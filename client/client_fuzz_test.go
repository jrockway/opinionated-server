//go:build go1.18
// +build go1.18

package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jrockway/opinionated-server/client/internal/fuzzsupport"
	"go.uber.org/zap/zaptest"
)

// cannedTransport is an http.RoundTripper that always returns the same response.
type cannedTransport struct {
	response *http.Response
}

func (t cannedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.response.Request = req
	if req.URL.Path == "/redirect" {
		// Avoid a redirect loop, sigh.
		h := make(http.Header)
		h.Add("content-type", "text/plain")
		return &http.Response{
			Status:     "OK",
			StatusCode: http.StatusOK,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}
	return t.response, nil
}

func FuzzLoggingTransportRoundTripResponse(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("\x04"))
	f.Add([]byte("\x1afoo-bar\x00baz\x01\x01foo\x01\x01bar\x00hello"))
	LogMetadata = true
	LogPayloads = true
	defer func() {
		LogMetadata = false
		LogPayloads = false
	}()
	f.Fuzz(func(t *testing.T, resBytes []byte) {
		genRes := new(fuzzsupport.GeneratedHTTPResponse)
		if err := genRes.UnmarshalText(resBytes); err != nil {
			t.Fatalf("unmarshal test case: %v", err)
		}
		transport := cannedTransport{response: (*http.Response)(genRes)}

		l := zaptest.NewLogger(t)
		client := &http.Client{
			Transport: WrapRoundTripperWithOptions(transport, WithLogger(l)),
		}
		req, err := http.NewRequestWithContext(context.Background(), "GET", "http://example.invalid/", http.NoBody)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("do: unexpected error: %v", err)
		}
		res.Body.Close()
	})
}
