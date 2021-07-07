package client

import (
	"errors"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
			Body:       ioutil.NopCloser(strings.NewReader("ok")),
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
		t.Errorf("wrapped roundtripper: GET: status:\n  got: %v\n want: %v", got, want)
	}
	if got, want := brt.count, 2; got != want {
		t.Errorf("request count:\n  got: %v\n want: %v", got, want)
	}

	res, _ = wrapped.RoundTrip(httptest.NewRequest("HEAD", "https://example.com", nil))
	if got, want := res.StatusCode, http.StatusOK; got != want {
		t.Errorf("wrapped roundtripper: HEAD: status:\n  got %v\n want: %v", got, want)
	}

	if _, err := wrapped.RoundTrip(httptest.NewRequest("PUT", "https://example.com", strings.NewReader("hi"))); err == nil {
		t.Errorf("expected error when calling PUT")
	}
}
