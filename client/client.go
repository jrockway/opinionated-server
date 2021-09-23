// Package client adds some goodies for clients inside opinionated-server servers.
//
// Nothing here should be called until server.Setup() has been run; we depend on the global tracer
// and global logger which are setup there.
package client

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	grpc_logging "github.com/grpc-ecosystem/go-grpc-middleware/logging"
	grpc_zap "github.com/grpc-ecosystem/go-grpc-middleware/logging/zap"
	"github.com/grpc-ecosystem/go-grpc-middleware/logging/zap/ctxzap"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/grpc-ecosystem/grpc-opentracing/go/otgrpc"
	"github.com/jrockway/opinionated-server/internal/formatters"
	"github.com/opentracing-contrib/go-stdlib/nethttp"
	"github.com/opentracing/opentracing-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	jaegerzap "github.com/uber/jaeger-client-go/log/zap"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

var (
	inFlightGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "http_client_in_flight_requests",
		Help: "A gauge of in-flight requests being made by a wrapped HTTP client.",
	})
	requestCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_client_api_requests_total",
		Help: "A counter of HTTP requests made by a wrapped HTTP client.",
	}, []string{"code", "method"})
)

var (
	ServerSetup = false
	LogMetadata = false
	LogPayloads = false
)

type loggingTransport struct {
	logger           *zap.Logger
	useContextLogger bool
	underlying       http.RoundTripper
}

type closeTracker struct {
	io.ReadCloser
	start       time.Time
	logger      *zap.Logger
	res         *http.Response
	err         error
	finishTrace func()
}

func (c *closeTracker) Close() error {
	defer inFlightGauge.Dec()
	defer c.finishTrace()
	l := c.logger.With(zap.Duration("duration", time.Since(c.start)))
	var closeErr error
	if c.ReadCloser != nil {
		if err := c.ReadCloser.Close(); err != nil {
			closeErr = err
			l = l.With(zap.NamedError("close_error", err))
		}
	}
	if LogMetadata && c.res != nil && c.res.Header != nil {
		l = l.With(zap.Array("response.headers", &formatters.MetadataWrapper{MD: c.res.Header}))
	}
	switch {
	case c.err != nil:
		l.Error("outgoing http request finished with error", zap.Error(c.err))
	case c.res != nil:
		l.Debug("outgoing http request finished", zap.Int("code", c.res.StatusCode), zap.String("status", c.res.Status))
	default:
		l.Error("outgoing http request succeeded, but returned nil response")
	}
	return closeErr
}

type roundtripFunc func(*http.Request) (*http.Response, error)

func (f roundtripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	inFlightGauge.Inc()
	// NOTE(jrockway): There is a slight flaw with this implementation.  The RoundTripper
	// doesn't follow redirects (the http.Client that calls into the RoundTripper handles that),
	// so if you get redirected you end up with two traces, instead of one trace that contains a
	// span for each redirect.  We'd have to write a custom Do method to handle that case, and
	// that wouldn't compose well with other middleware you might want.
	req, tr := nethttp.TraceRequest(opentracing.GlobalTracer(), req)
	l := t.logger
	if l == nil {
		if t.useContextLogger {
			l = ctxzap.Extract(req.Context())
		} else {
			l = zap.L().Named("http_client")
		}
	}
	l = l.With(zap.String("method", req.Method), zap.String("url", req.URL.String()))
	start := time.Now()
	rt := &nethttp.Transport{
		RoundTripper: roundtripFunc(func(req *http.Request) (*http.Response, error) {
			// We're here because we need to get the tracing headers that
			// nethttp.Transport created in order to produce the most useful log
			// message.  (But we have to be outside of its RoundTrip function to call
			// nethttp.TraceRequest.)
			//
			// Regrettably, they don't add the trace to the request context, so don't
			// get the outgoing span ID with jaegerzap.Trace here.  I'll fix that
			// upstream (https://github.com/opentracing-contrib/go-stdlib/pull/62).
			l = l.With(jaegerzap.Trace(req.Context()))
			ct := &closeTracker{
				start:       start,
				logger:      l,
				finishTrace: tr.Finish,
			}
			l.Debug("outgoing http request", zap.Array("request.headers", &formatters.MetadataWrapper{MD: req.Header}))
			req = req.WithContext(ctxzap.ToContext(req.Context(), l))
			res, err := t.underlying.RoundTrip(req)
			ct.res = res
			ct.err = err
			if res != nil {
				ct.ReadCloser = res.Body
				res.Body = ct
				if err == nil {
					code := strconv.Itoa(res.StatusCode)
					method := strings.ToLower(req.Method)
					requestCount.WithLabelValues(method, code).Inc()
				}
			}
			if req.Method == "HEAD" || res == nil {
				ct.Close()
			}
			return res, err
		}),
	}
	return rt.RoundTrip(req)
}

type roundTripperOptions struct {
	logger        *zap.Logger
	contextLogger bool
}

// RoundTripperOption customizes the behavior of the RoundTripper returned by WrapRoundTripper.
type RoundTripperOption func(*roundTripperOptions)

// WithLogger causes the RoundTripper returned by WrapRoundTripper to log requests to the provided
// log, instead of a global logger.
func WithLogger(l *zap.Logger) RoundTripperOption {
	return func(o *roundTripperOptions) {
		o.logger = l
	}
}

// WithContextLogger causes the RoundTripper returned by WrapRoundTripper to log requests to the
// context logger (ctxzap), instead of the global logger.  Note that logs will be suppressed if
// there is no logger in the context, because ctxzap returns a NoopLogger and we can't inspect that
// logger and upgrade it to a real logger.
//
// If WithLogger is specified, this option is ignored and a warning is logged when creating the
// roundtripper.
func WithContextLogger() RoundTripperOption {
	return func(o *roundTripperOptions) {
		o.contextLogger = true
	}
}

// WrapRoundTripper returns a wrapped version of the provided RoundTripper with tracing and
// prometheus metrics.  The RoundTripper may be nil, in which case an empty http.Transport will be
// used.
func WrapRoundTripper(rt http.RoundTripper, options ...RoundTripperOption) http.RoundTripper {
	if rt == nil {
		rt = &http.Transport{}
	}
	rtopts := new(roundTripperOptions)
	for _, opt := range options {
		opt(rtopts)
	}
	if rtopts.logger != nil && rtopts.contextLogger {
		rtopts.logger.Warn("ignoring WithContextLogger option passed to client.WrapRoundTripper")
	}
	return &loggingTransport{
		logger:           rtopts.logger,
		useContextLogger: rtopts.contextLogger,
		underlying:       rt,
	}
}

// GRPCInterceptors returns interceptors that you should use when dialing a remote gRPC service,
// including instrumentation to log each request and propagate tracing information upstream.  Only
// call this after server.Setup() has been run; we rely on the global logger and global tracer setup
// there.
//
// We give you this list instead of a DialOption (or wrap Dial ourselves) so that you don't lose the
// ability to add your own interceptors.  (grpc.WithChainUnaryInterceptor will let you pass a list
// of interceptors to Dial.)
func GRPCInterceptors() ([]grpc.UnaryClientInterceptor, []grpc.StreamClientInterceptor) {
	if !ServerSetup {
		panic("server has not been setup with server.Setup(); call that first")
	}

	l := zap.L().Named("grpc_client")
	unary := []grpc.UnaryClientInterceptor{
		otgrpc.OpenTracingClientInterceptor(opentracing.GlobalTracer()),
		grpc_prometheus.UnaryClientInterceptor,
		// I hate some of the choices grpc_zap makes, but for now, it's OK.
		grpc_zap.UnaryClientInterceptor(l, grpc_zap.WithCodes(grpc_logging.DefaultErrorToCode), grpc_zap.WithDurationField(grpc_zap.DurationToDurationField)),
	}
	stream := []grpc.StreamClientInterceptor{
		otgrpc.OpenTracingStreamClientInterceptor(opentracing.GlobalTracer()),
		grpc_prometheus.StreamClientInterceptor,
		grpc_zap.StreamClientInterceptor(l, grpc_zap.WithCodes(grpc_logging.DefaultErrorToCode), grpc_zap.WithDurationField(grpc_zap.DurationToDurationField)),
	}
	decider := func(ctx context.Context, fullMethodName string) bool { return true }
	if LogPayloads {
		unary = append(unary, grpc_zap.PayloadUnaryClientInterceptor(l, decider))
		stream = append(stream, grpc_zap.PayloadStreamClientInterceptor(l, decider))
	}
	return unary, stream
}
