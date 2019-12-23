package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

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
	web := http.NewServeMux()
	web.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
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
		t.Logf("server listening on: %v", info)
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
		conn, err := grpc.DialContext(ctx, info.GRPCAddress, grpc.WithBlock(), grpc.WithInsecure())
		if err != nil {
			t.Fatalf("dial grpc %q: %v", info.GRPCAddress, err)
		}
		if err := isHealthy(ctx, conn); err != nil {
			t.Errorf("grpc endpoint unhealthy: %v", err)
		}

		req, err := http.NewRequest("get", "http://"+info.DebugAddress+"/metrics", nil)
		if err != nil {
			t.Fatalf("new debug request: %v", err)
		}
		req = req.WithContext(ctx)
		res, err := http.DefaultClient.Do(req)
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
	})
}

func TestHTTPServer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	SetHTTPHandler(mux)
	defer func() { httpHandler = nil }()

	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	runServerTest(ctx, t, func(t *testing.T, info Info) {
		req, err := http.NewRequest("get", "http://"+info.HTTPAddress+"/", nil)
		if err != nil {
			t.Fatalf("new http request: %v", err)
		}
		req = req.WithContext(ctx)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request /: %v", err)
		}
		if got, want := res.StatusCode, http.StatusOK; got != want {
			t.Errorf("request /: status:\n  got: %v\n want: %v", got, want)
		}
		res.Body.Close()
	})
}
