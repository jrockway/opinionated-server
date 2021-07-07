// Package client adds some goodies for clients inside opinionated-server servers.
//
// Nothing here should be called until server.Setup() has been run; we depend on the global tracer
// and global logger which are setup there.
package client

import (
	"context"
	"io"
	"net/http"
	"time"

	grpc_logging "github.com/grpc-ecosystem/go-grpc-middleware/logging"
	grpc_zap "github.com/grpc-ecosystem/go-grpc-middleware/logging/zap"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/grpc-ecosystem/grpc-opentracing/go/otgrpc"
	"github.com/jrockway/opinionated-server/internal/formatters"
	"github.com/opentracing-contrib/go-stdlib/nethttp"
	"github.com/opentracing/opentracing-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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
	underlying http.RoundTripper
}

type closeTracker struct {
	io.ReadCloser
	start  time.Time
	logger *zap.Logger
	res    *http.Response
	err    error
}

func (c *closeTracker) Close() error {
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

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	l := zap.L().With(zap.String("method", req.Method), zap.String("url", req.URL.String()))
	reqLogger := l
	if LogMetadata {
		reqLogger = l.With(zap.Array("request.headers", &formatters.MetadataWrapper{MD: req.Header}))
	}
	reqLogger.Debug("outgoing http request")
	ct := &closeTracker{
		start:  time.Now(),
		logger: l,
	}
	res, err := t.underlying.RoundTrip(req)
	ct.res = res
	ct.err = err
	if res != nil {
		ct.ReadCloser = res.Body
		res.Body = ct
	}

	if req.Method == "HEAD" || res == nil {
		ct.Close()
	}
	return res, err
}

type tracingTransport struct {
	underlying http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *tracingTransport) RoundTrip(orig *http.Request) (*http.Response, error) {
	req, tr := nethttp.TraceRequest(opentracing.GlobalTracer(), orig)
	defer tr.Finish()
	return t.underlying.RoundTrip(req)
}

// WrapRoundTripper returns a wrapped version of the provided RoundTripper with tracing and
// prometheus metrics.  The RoundTripper may be nil, in which case an empty http.Transport will be
// used.
func WrapRoundTripper(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = &http.Transport{}
	}
	return &tracingTransport{
		underlying: promhttp.InstrumentRoundTripperInFlight(inFlightGauge,
			promhttp.InstrumentRoundTripperCounter(
				requestCount, &nethttp.Transport{
					RoundTripper: &loggingTransport{
						underlying: rt,
					}})),
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
