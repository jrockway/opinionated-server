package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jrockway/opinionated-server/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func isHealthy(ctx context.Context, conn *grpc.ClientConn) error {
	health := grpc_health_v1.NewHealthClient(conn)
	res, err := health.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		return fmt.Errorf("check health: %w", err)
	}
	if s := res.GetStatus(); s != grpc_health_v1.HealthCheckResponse_SERVING {
		return fmt.Errorf("check health: unhealthy status: %s", s.String())
	}
	return nil
}

func runServerTest(ctx context.Context, t *testing.T, test func(t *testing.T, info Info)) {
	logOpts.LogMetadata = true
	logOpts.LogPayloads = true
	logOpts.LogLevel = "debug"
	defer func() {
		logOpts.LogMetadata = false
		logOpts.LogPayloads = false
	}()

	// Serve the grpc health handler.
	serviceHooks = []func(s *grpc.Server){func(s *grpc.Server) {}}

	os.Setenv("JAEGER_SAMPLER_TYPE", "const")
	os.Setenv("JAEGER_SAMPLER_PARAM", "0")
	if err := setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	infoCh := make(chan Info)
	SetStartupCallback(func(info Info) { infoCh <- info })
	defer func() { startupCallback = nil }()

	doneCh := make(chan error)
	killCh := make(chan string)
	go func() {
		err := listenAndServe(killCh)
		doneCh <- err
	}()

	var info Info
	select {
	case <-ctx.Done():
		t.Fatalf("timeout waiting for startup: %v", ctx.Err())
	case err := <-doneCh:
		t.Fatalf("server exited before calling startup callback: %v", err)
	case info = <-infoCh:
	}

	test(t, info)

	killCh <- "tests done"
	select {
	case <-ctx.Done():
		t.Errorf("timeout waiting for shutdown: %v", ctx.Err())
	case err := <-doneCh:
		if err != nil {
			t.Errorf("listenAndServe: %v", err)
		}
	}
}

func TestDefaultServers(t *testing.T) {
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	runServerTest(ctx, t, func(t *testing.T, info Info) {
		unaryI, streamI := client.GRPCInterceptors()
		conn, err := grpc.DialContext(ctx, info.GRPCAddress, grpc.WithBlock(), grpc.WithInsecure(), grpc.WithChainUnaryInterceptor(unaryI...), grpc.WithChainStreamInterceptor(streamI...))
		if err != nil {
			t.Fatalf("dial grpc %q: %v", info.GRPCAddress, err)
		}
		if err := isHealthy(ctx, conn); err != nil {
			t.Errorf("grpc endpoint unhealthy: %v", err)
		}

		httpClient := &http.Client{Transport: client.WrapRoundTripper(http.DefaultTransport)}
		req, err := http.NewRequest("get", "http://"+info.DebugAddress+"/metrics", http.NoBody)
		if err != nil {
			t.Fatalf("new debug request: %v", err)
		}
		req = req.WithContext(ctx)
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("request /metrics: %v", err)
		}
		if got, want := res.StatusCode, http.StatusOK; got != want {
			t.Errorf("request /metrics: status:\n  got: %v\n want: %v", got, want)
		}
		res.Body.Close()

		if got, want := info.HTTPAddress, ""; got != want {
			t.Errorf("http address:\n  got: %v\n want: %v", got, want)
		}
		conn.Close()
	})
}

func TestHTTPServer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Add("content-type", "text/plain")
		w.Header().Add("x-foo-bar", "baz")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	SetHTTPHandler(mux)
	defer func() { httpHandler = nil }()

	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	runServerTest(ctx, t, func(t *testing.T, info Info) {
		logOpts.LogMetadata = true
		defer func() {
			logOpts.LogMetadata = false
		}()

		httpClient := &http.Client{Transport: client.WrapRoundTripper(http.DefaultTransport)}
		req, err := http.NewRequest("get", "http://"+info.HTTPAddress+"/", http.NoBody)
		if err != nil {
			t.Fatalf("new http request: %v", err)
		}
		req = req.WithContext(ctx)
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("request /: %v", err)
		}
		if got, want := res.StatusCode, http.StatusOK; got != want {
			t.Errorf("request /: status:\n  got: %v\n want: %v", got, want)
		}
		res.Body.Close()
	})
}

func TestDrain(t *testing.T) {
	listenOpts.PreDrainGracePeriod = 100 * time.Millisecond
	listenOpts.ShutdownGracePeriod = time.Second
	logOpts.LogMetadata = true
	logOpts.LogPayloads = true
	logOpts.LogLevel = "debug"
	defer func() {
		logOpts.LogMetadata = false
		logOpts.LogPayloads = false
	}()

	infoCh := make(chan Info)
	SetStartupCallback(func(info Info) { infoCh <- info })
	defer func() { startupCallback = nil }()

	drainCh := make(chan struct{})
	AddDrainHandler(func() { close(drainCh) })
	defer func() { drainHandlers = nil }()

	http.HandleFunc("/wait-for-drain", func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		select {
		case <-drainCh:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("draining"))
		case <-ctx.Done():
			http.Error(w, ctx.Err().Error(), http.StatusRequestTimeout)
		}
	})

	killCh := make(chan string)
	serverDoneCh := make(chan error)
	go func() { serverDoneCh <- listenAndServe(killCh) }()

	var info Info
	select {
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for startup")
	case info = <-infoCh:
		close(infoCh)
	}
	time.AfterFunc(200*time.Millisecond, func() { killCh <- "force drain" })

	httpClient := &http.Client{Transport: client.WrapRoundTripper(http.DefaultTransport)}
	req, err := http.NewRequestWithContext(context.TODO(), "GET", "http://"+info.DebugAddress+"/wait-for-drain", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got, want := res.StatusCode, http.StatusOK; got != want {
		t.Errorf("get /wait-for-drain: response status:\n  got: %v\n want: %v", got, want)
	}
}
