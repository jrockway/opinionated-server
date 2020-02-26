package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type boringRoundTripper struct {
	count int
}

func (rt *boringRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	rt.count++
	return &http.Response{
		Status:     "OK",
		StatusCode: http.StatusOK,
		Header:     http.Header{},
	}, nil
}

func TestWrapRoundTripper(t *testing.T) {
	brt := &boringRoundTripper{}
	req := httptest.NewRequest("get", "https://example.com/", nil)

	res, _ := brt.RoundTrip(req)
	if got, want := res.StatusCode, http.StatusOK; got != want {
		t.Errorf("basic roundtripper: status\n  got: %v\n want: %v", got, want)
	}
	if got, want := brt.count, 1; got != want {
		t.Errorf("request count:\n  got: %v\n want: %v", got, want)
	}

	wrapped := WrapRoundTripper(brt)
	res, _ = wrapped.RoundTrip(req)
	if got, want := res.StatusCode, http.StatusOK; got != want {
		t.Errorf("basic roundtripper: status\n  got: %v\n want: %v", got, want)
	}
	if got, want := brt.count, 2; got != want {
		t.Errorf("request count:\n  got: %v\n want: %v", got, want)
	}
}
