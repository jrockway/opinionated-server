//go:build go1.18
// +build go1.18

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jrockway/opinionated-server/internal/fuzzsupport"
	"github.com/opentracing-contrib/go-stdlib/nethttp"
	"github.com/opentracing/opentracing-go"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func FuzzHTTPServer(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("\x02\x03key\x00value\x00foo"))
	f.Fuzz(func(t *testing.T, reqBytes []byte) {
		doneCh := make(chan struct{}, 1)
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
			w.Header().Add("content-type", "text/plain")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
			doneCh <- struct{}{}
		})
		SetHTTPHandler(mux)
		defer func() { httpHandler = nil }()
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		runServerTest(ctx, t, func(info Info) {
			greq := new(fuzzsupport.GeneratedHTTPRequest)
			if err := greq.UnmarshalText(reqBytes); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			req := (*http.Request)(greq).WithContext(ctx)
			req.URL = &url.URL{
				Scheme: "http",
				Host:   info.HTTPAddress,
				Path:   "/",
			}
			w := httptest.NewRecorder()

			// Unforutunately copied from listenAndServe.
			server := h2c.NewHandler(nethttp.Middleware(opentracing.GlobalTracer(), instrumentHandler("http", httpHandler), nethttp.MWComponentName("http")), &http2.Server{})

			server.ServeHTTP(w, req)

			if got, want := w.Code, http.StatusOK; got != want {
				t.Errorf("request /: status:\n  got: %v\n want: %v", got, want)
			}
			select {
			case <-doneCh:
				return
			case <-time.After(5 * time.Second):
				t.Fatal("handler not run after 5 seconds")
			}
		})
	})
}
