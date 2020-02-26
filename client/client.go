// Package client adds some goodies for clients inside opinionated-server servers.
package client

import (
	"net/http"

	"github.com/opentracing-contrib/go-stdlib/nethttp"
	"github.com/opentracing/opentracing-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

type transport struct {
	underlying http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *transport) RoundTrip(orig *http.Request) (*http.Response, error) {
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
	return &transport{
		underlying: promhttp.InstrumentRoundTripperInFlight(inFlightGauge,
			promhttp.InstrumentRoundTripperCounter(
				requestCount, &nethttp.Transport{
					RoundTripper: rt})),
	}
}
