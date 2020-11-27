package client

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
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
		Body:       ioutil.NopCloser(strings.NewReader("ok")),
	}, nil
}

func TestWrapRoundTripper(t *testing.T) {
	brt := &boringRoundTripper{}
	req := httptest.NewRequest("GET", "https://example.com/", nil)

	res, _ := brt.RoundTrip(req)
	if got, want := res.StatusCode, http.StatusOK; got != want {
		t.Errorf("basic roundtripper: status\n  got: %v\n want: %v", got, want)
	}
	res.Body.Close()
	if got, want := brt.count, 1; got != want {
		t.Errorf("request count:\n  got: %v\n want: %v", got, want)
	}

	wrapped := WrapRoundTripper(brt)
	res, _ = wrapped.RoundTrip(req)
	res.Body.Close()
	if got, want := res.StatusCode, http.StatusOK; got != want {
		t.Errorf("basic roundtripper: status\n  got: %v\n want: %v", got, want)
	}
	if got, want := brt.count, 2; got != want {
		t.Errorf("request count:\n  got: %v\n want: %v", got, want)
	}
}
