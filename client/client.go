// Package client adds some goodies for clients inside opinionated-server servers.
package client

import (
	"net/http"
	"time"

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
	// UnaryInterceptors is setup when you call server.Setup()
	UnaryInterceptors []grpc.UnaryClientInterceptor
	// StreamInterceptors is setup when you call server.Setup()
	StreamInterceptors []grpc.StreamClientInterceptor
)

type loggingTransport struct {
	underlying http.RoundTripper
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	l := zap.L().With(zap.String("method", req.Method), zap.String("url", req.URL.String()))
	l.Debug("outgoing http request")
	start := time.Now()
	res, err := t.underlying.RoundTrip(req)
	l = l.With(zap.Duration("duration", time.Since(start)))
	switch {
	case err != nil:
		l.Error("outgoing http request done", zap.Error(err))
	case res != nil:
		l.Debug("outgoing http request done", zap.Int("status.code", res.StatusCode), zap.String("status", res.Status))
	default:
		l.Error("outgoing http request succeeded, but returned nil response")
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

// GRPCInterceptors returns interceptors that you should use when dialing a remote gRPC service.  We
// give you this list instead of a DialOption (or wrap Dial ourselves) so that you don't lose the
// ability to add your own interceptors.  (grpc.WithChainUnaryInterceptor is the DialOption you're
// looking for.)
func GRPCInterceptors() ([]grpc.UnaryClientInterceptor, []grpc.StreamClientInterceptor) {
	return UnaryInterceptors, StreamInterceptors
}
