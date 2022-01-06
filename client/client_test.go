package client

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opentracing/opentracing-go"
	"github.com/uber/jaeger-client-go/config"
	jaegerzap "github.com/uber/jaeger-client-go/log/zap"
	"github.com/uber/jaeger-client-go/zipkin"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
)

type boringRoundTripper struct {
	count int
}

func (rt *boringRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.count++
	switch req.Method {
	case "GET":
		return &http.Response{
			Status:     "OK",
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	case "HEAD":
		return &http.Response{
			Status:     "OK",
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       nil,
		}, nil
	default:
		return nil, errors.New("not implemented")
	}
}

func TestWrapRoundTripper(t *testing.T) {
	l := zaptest.NewLogger(t, zaptest.Level(zapcore.DebugLevel))
	jcfg := config.Configuration{
		ServiceName: "tests",
		Sampler: &config.SamplerConfig{
			Type:  "const",
			Param: 1,
		},
		Reporter: &config.ReporterConfig{
			LogSpans: true,
		},
	}
	tracer, closer, err := jcfg.NewTracer(
		config.Logger(jaegerzap.NewLogger(l.Named("jaeger"))),
		config.Injector(opentracing.HTTPHeaders, zipkin.NewZipkinB3HTTPHeaderPropagator()))
	if err != nil {
		t.Fatalf("start jaeger: %v", err)
	}
	opentracing.SetGlobalTracer(tracer)
	defer opentracing.SetGlobalTracer(opentracing.NoopTracer{})
	defer closer.Close()

	brt := &boringRoundTripper{}

	req := httptest.NewRequest("GET", "https://example.com/", http.NoBody)
	res, _ := brt.RoundTrip(req)
	if got, want := res.StatusCode, http.StatusOK; got != want {
		t.Errorf("basic roundtripper: status\n  got: %v\n want: %v", got, want)
	}
	res.Body.Close()
	if got, want := brt.count, 1; got != want {
		t.Errorf("request count:\n  got: %v\n want: %v", got, want)
	}

	wrapped := WrapRoundTripperWithOptions(brt, WithLogger(l.Named("test")))

	res, _ = wrapped.RoundTrip(req)
	res.Body.Close()
	if got, want := res.StatusCode, http.StatusOK; got != want {
		t.Errorf("wrapped roundtripper: GET: status:\n  got: %v\n want: %v", got, want)
	}
	if got, want := brt.count, 2; got != want {
		t.Errorf("request count:\n  got: %v\n want: %v", got, want)
	}

	res, _ = wrapped.RoundTrip(httptest.NewRequest("HEAD", "https://example.com", nil))
	if got, want := res.StatusCode, http.StatusOK; got != want {
		t.Errorf("wrapped roundtripper: HEAD: status:\n  got %v\n want: %v", got, want)
	}
	res.Body.Close()

	res, err = wrapped.RoundTrip(httptest.NewRequest("PUT", "https://example.com", strings.NewReader("hi")))
	if err == nil {
		t.Errorf("expected error when calling PUT")
		res.Body.Close()
	}
}
