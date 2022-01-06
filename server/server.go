// Package server initializes an RPC server app, providing gRPC, HTTP, and debug HTTP servers, Jaeger
// tracing, Zap logging, and Prometheus monitoring.
package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	_ "net/http/pprof" // nolint
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	grpc_zap "github.com/grpc-ecosystem/go-grpc-middleware/logging/zap"
	"github.com/grpc-ecosystem/go-grpc-middleware/logging/zap/ctxzap"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/grpc-ecosystem/grpc-opentracing/go/otgrpc"
	"github.com/jessevdk/go-flags"
	"github.com/jrockway/opinionated-server/client"
	"github.com/opentracing-contrib/go-stdlib/nethttp"
	"github.com/opentracing/opentracing-go"
	"github.com/povilasv/prommod"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	jaegercfg "github.com/uber/jaeger-client-go/config"
	jaegerzap "github.com/uber/jaeger-client-go/log/zap"
	"github.com/uber/jaeger-client-go/zipkin"
	jprom "github.com/uber/jaeger-lib/metrics/prometheus"
	"go.uber.org/automaxprocs/maxprocs"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	channelz "google.golang.org/grpc/channelz/service"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// Info is provided to an (optional) callback after the server has started.  It is mostly
// useful for tests, but is exposed in case you want to do something after the server has started
// serving.
type Info struct {
	HTTPAddress, DebugAddress, GRPCAddress string
}

// InterceptorContext is some state passed to interceptor creation functions registered with
// AddUnaryInterceptorFn and AddStreamInterceptorFn.
type InterceptorContext struct {
	Tracer opentracing.Tracer // The opentracing tracer to be used to trace requests.
	Logger *zap.Logger        // The Zap logger to be used for trace information.  It will never be nil.
}

var (
	AppName          = "server"
	AppVersion       = "unversioned"
	appVersionMetric = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "app_version",
			Help: "The version string that was compiled into this app.",
		},
		[]string{"name", "version"},
	)

	flagParser      = flags.NewParser(nil, flags.HelpFlag|flags.PassDoubleDash)
	logOpts         = &logOptions{}
	logLevel        zap.AtomicLevel
	listenOpts      = &listenOptions{}
	restoreLogger   = func() {}
	flushTraces     io.Closer
	httpHandler     http.Handler
	serviceHooks    []func(s *grpc.Server)
	startupCallback func(Info)
	drainHandlers   []func()

	healthServer     = health.NewServer()
	debugSetup       = false
	tracingSetup     = false
	grpcLogInstalled = false

	// Interceptors we'd like to add.  We defer evaluation until starting the server, because
	// things like opentracing.GlobalTracer() will be incorrect until then.
	unaryInterceptorFns = []func(ic *InterceptorContext) grpc.UnaryServerInterceptor{
		func(ic *InterceptorContext) grpc.UnaryServerInterceptor {
			return otgrpc.OpenTracingServerInterceptor(ic.Tracer, otgrpc.IncludingSpans(shouldTrace))
		},
		func(ic *InterceptorContext) grpc.UnaryServerInterceptor {
			return grpc_prometheus.UnaryServerInterceptor
		},
		func(ic *InterceptorContext) grpc.UnaryServerInterceptor { return loggingUnaryServerInterceptor() },
	}
	streamInterceptorFns = []func(ic *InterceptorContext) grpc.StreamServerInterceptor{
		func(ic *InterceptorContext) grpc.StreamServerInterceptor {
			return otgrpc.OpenTracingStreamServerInterceptor(ic.Tracer, otgrpc.IncludingSpans(shouldTrace))
		},
		func(ic *InterceptorContext) grpc.StreamServerInterceptor {
			return grpc_prometheus.StreamServerInterceptor
		},
		func(ic *InterceptorContext) grpc.StreamServerInterceptor { return loggingStreamServerInterceptor() },
	}
)

type logOptions struct {
	LogLevel       string `long:"log_level" description:"zap level to log at" default:"debug" env:"LOG_LEVEL"`
	GRPCVerbosity  int    `long:"grpc_verbosity" description:"verbosity level of grpc logs" default:"0" env:"GRPC_GO_LOG_VERBOSITY_LEVEL"`
	DevelopmentLog bool   `long:"pretty_logs" description:"use the nicer-to-look at development log" env:"PRETTY_LOGS"`

	LogMetadata bool `long:"log_metadata" description:"log headers/metadata for each http or grpc request" env:"LOG_METADATA"`
	LogPayloads bool `long:"log_payloads" description:"log requests and responses for each http or grpc request; if true, payloads are logged to the logger and reported to jaeger" env:"LOG_PAYLOADS"`

	LogTraces bool `long:"log_traces" description:"if true, log verbose Jaeger debugging information (more verbose than JAEGER_REPORTER_LOG_SPANS=1)" env:"LOG_TRACES"`
}

type listenOptions struct {
	HTTPAddress         string        `long:"http_address" description:"address to listen for http requests on" default:"0.0.0.0:8080" env:"HTTP_ADDRESS"`
	DebugAddress        string        `long:"debug_address" description:"address to listen for debug http requests on" default:"127.0.0.1:8081" env:"DEBUG_ADDRESS"`
	GRPCAddress         string        `long:"grpc_address" description:"address to listen for grpc requests on" default:"0.0.0.0:9000" env:"GRPC_ADDRESS"`
	PreDrainGracePeriod time.Duration `long:"predrain_grace_period" description:"how long to wait after receiving an exit signal before draining; used to work around long 'network reprogramming' latency" default:"1s" env:"PREDRAIN_GRACE_PERIOD"`
	ShutdownGracePeriod time.Duration `long:"shutdown_grace_period" description:"how long to wait on draining connections before exiting" default:"30s" env:"SHUTDOWN_GRACE_PERIOD"`
}

// AddFlagGroup lets you add your own flags to be parsed with the server-level flags.
func AddFlagGroup(name string, data interface{}) {
	_, err := flagParser.AddGroup(name, "", data)
	if err != nil {
		panic(fmt.Sprintf("add flag group %q: %v", name, err))
	}
}

// Setup sets up the necessary global configuration for your server app.  It parses
// flags/environment variables, and initializes logging, tracing, etc.
//
// If there is a problem, we kill the program.
func Setup() {
	if _, err := flagParser.AddGroup("Addresses", "", listenOpts); err != nil {
		panic(err)
	}
	if _, err := flagParser.AddGroup("Logging", "", logOpts); err != nil {
		panic(err)
	}
	if _, err := flagParser.Parse(); err != nil {
		if ferr, ok := err.(*flags.Error); ok && ferr.Type == flags.ErrHelp {
			fmt.Fprintf(os.Stderr, "%s version %s\n", AppName, AppVersion)
			fmt.Fprintf(os.Stderr, ferr.Message)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "flag parsing: %v\n", err)
		os.Exit(3)
	}

	if err := setup(); err != nil {
		zap.L().Fatal("error initializing app", zap.Error(err))
	}
	appVersionMetric.WithLabelValues(AppName, AppVersion).Set(1)
	prometheus.Register(prommod.NewCollector(AppName))
	zap.L().Info("app starting", zap.String("name", AppName), zap.String("version", AppVersion))
}

func maxprocsLogger() maxprocs.Option {
	l := zap.L().Named("maxprocs").WithOptions(zap.AddCallerSkip(1)).Sugar()
	return maxprocs.Logger(func(msg string, args ...interface{}) {
		l.Infof(msg, args...)
	})
}

func setup() error {
	if listenOpts.ShutdownGracePeriod == 0 {
		listenOpts.ShutdownGracePeriod = time.Second
	}
	if err := setupLogging(); err != nil {
		return fmt.Errorf("setup logging: %w", err)
	}
	if _, err := maxprocs.Set(maxprocsLogger()); err != nil {
		return fmt.Errorf("setup maxprocs: %w", err)
	}
	if err := setupTracing(); err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}
	setupDebug()
	setupClient()
	return nil
}

func setupLogging() error {
	lcfg := zap.NewProductionConfig()
	if logOpts.DevelopmentLog {
		lcfg = zap.NewDevelopmentConfig()
	}
	logger, err := lcfg.Build()
	if err != nil {
		return fmt.Errorf("init zap: %w", err)
	}
	restoreLogger = zap.ReplaceGlobals(logger)
	zap.RedirectStdLog(logger)
	logLevel = lcfg.Level
	if err := logLevel.UnmarshalText([]byte(logOpts.LogLevel)); err != nil {
		return fmt.Errorf("set log level: %w", err)
	}
	if !grpcLogInstalled {
		grpcLogInstalled = true
		grpc_zap.ReplaceGrpcLoggerV2WithVerbosity(zap.L().Named("grpc").WithOptions(zap.AddCallerSkip(2)), logOpts.GRPCVerbosity)
	}
	return nil
}

func setupTracing() error {
	if tracingSetup {
		return nil
	}

	jcfg, err := jaegercfg.FromEnv()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if jcfg.ServiceName == "" {
		jcfg.ServiceName = AppName
	}
	zipkinPropagator := zipkin.NewZipkinB3HTTPHeaderPropagator()
	options := []jaegercfg.Option{
		jaegercfg.Logger(jaegerzap.NewLogger(zap.L().Named("jaeger").WithOptions(zap.AddCallerSkip(1)))),
		jaegercfg.Metrics(jprom.New()),
		jaegercfg.Injector(opentracing.HTTPHeaders, zipkinPropagator),
		jaegercfg.Extractor(opentracing.HTTPHeaders, zipkinPropagator),
	}
	tracer, closer, err := jcfg.NewTracer(options...)
	if err != nil {
		return fmt.Errorf("tracer: %v", err)
	}
	if logOpts.LogTraces {
		tracer = jaegerzap.NewLoggingTracer(zap.L().Named("jaeger_debug"), tracer)
	}
	opentracing.SetGlobalTracer(tracer)
	flushTraces = closer
	tracingSetup = true
	return nil
}

func setupDebug() {
	if debugSetup {
		return
	}
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/zap", func(w http.ResponseWriter, req *http.Request) {
		before := logLevel.Level().CapitalString()
		logLevel.ServeHTTP(w, req)
		after := logLevel.Level().CapitalString()
		if before != after {
			ctxzap.Extract(req.Context()).Info("set log level", zap.String("original", before), zap.String("new", after))
		}
	})
	http.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
		reply, err := healthServer.Check(req.Context(), &grpc_health_v1.HealthCheckRequest{
			Service: "",
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		status := reply.GetStatus()
		if status != grpc_health_v1.HealthCheckResponse_SERVING {
			http.Error(w, status.String(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(status.String()))
	})
	debugSetup = true
}

func setupClient() {
	client.LogMetadata = logOpts.LogMetadata
	client.LogPayloads = logOpts.LogPayloads
	client.ServerSetup = true
}

// AddService registers a gRPC server to be run by the RPC server.  It is intended to be used like:
//
//   server.AddService(func (s *grpc.Server) { my_proto.RegisterMyService(s, myImplementation) })
func AddService(cb func(s *grpc.Server)) {
	serviceHooks = append(serviceHooks, cb)
}

// SetHTTPHandler registers an HTTP handler to serve all non-debug HTTP requests.  You may only
// register a single handler; to serve multiple URLs, use an http.ServeMux.
func SetHTTPHandler(h http.Handler) {
	if httpHandler != nil {
		panic("attempt to add an http handler with one already registered")
	}
	httpHandler = h
}

// SetStartupCallback registers a function to be called when the server starts.
func SetStartupCallback(cb func(Info)) {
	if startupCallback != nil {
		panic("attempt to add a startup callback with one already registered")
	}
	startupCallback = cb
}

// AddDrainHandler registers a function to be called when the server begins draining.  It is not
// safe to add a drain handler while ListenAndServe is running.
//
// The provided drain handler will be called while your server is still serving, allowing you to
// cleanly interrupt long-lived requests.  If your drain handler blocks, it will interfere with a
// clean shutdown, so don't block.  Your handlers will have the configured grace period to react to
// the drain event.
//
// To cancel select statements, share a channel between the drain handler and your event loop:
//
// 	drainCh := make(chan struct{})
// 	server.AddDrainHandler(func() { close(drainCh) })
// 	...
// 	server.ListenAndServe()
//
// Then in some long-lived handler:
//
//	for {
//		select {
//		case <-drainCh:
//              	return errors.New("draining")
//		case <-ctx.Done():
//              	return ctx.Err()
//		case <-whatever:
//			// whatever
//		}
//	}
func AddDrainHandler(f func()) {
	drainHandlers = append(drainHandlers, f)
}

// AddUnaryInterceptorFn adds a unary interceptor, computed by f at server startup time, to the server.
func AddUnaryInterceptorFn(f func(ic *InterceptorContext) grpc.UnaryServerInterceptor) {
	unaryInterceptorFns = append(unaryInterceptorFns, f)
}

// AddUnaryInterceptor adds a grpc unary interceptor to the server.
func AddUnaryInterceptor(i grpc.UnaryServerInterceptor) {
	AddUnaryInterceptorFn(func(ic *InterceptorContext) grpc.UnaryServerInterceptor { return i })
}

// AddStreamInterceptorFn adds a stream interceptor, computed by f at server startup time, to the server.
func AddStreamInterceptorFn(f func(ic *InterceptorContext) grpc.StreamServerInterceptor) {
	streamInterceptorFns = append(streamInterceptorFns, f)
}

// AddStreamInterceptor adds a grpc stream interceptor to the server.
func AddStreamInterceptor(i grpc.StreamServerInterceptor) {
	AddStreamInterceptorFn(func(ic *InterceptorContext) grpc.StreamServerInterceptor { return i })
}

// isNotMonitoring returns true if the request is not monitoring.  (This is to suppress tracing of
// kubelet health checks and prometheus metric scrapes.)
func isNotMonitoring(req *http.Request) bool {
	if strings.HasPrefix(req.Header.Get("User-Agent"), "kube-probe/") {
		return false
	}
	if req.URL != nil && (req.URL.Path == "/metrics" || req.URL.Path == "/healthz") {
		return false
	}
	return true
}

func suppressInstrumentation(method string) bool {
	return strings.HasPrefix(method, "/grpc.health.v1.Health/")
}

func shouldTrace(spanCtx opentracing.SpanContext, method string, req, _ interface{}) bool {
	if spanCtx != nil {
		return true
	}
	return !suppressInstrumentation(method)
}

var (
	httpInFlightGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "http_in_flight",
			Help: "A gauge of requests currently being served by the wrapped handler.",
		},
		[]string{"handler"},
	)

	httpCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "A counter for requests to the wrapped handler.",
		},
		[]string{"handler", "code", "method"},
	)

	httpDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "A histogram of latencies for requests.",
			Buckets: []float64{0.0005, 0.001, 0.01, 0.1, 0.2, 0.4, 0.8, 1, 1.5, 2, 3, 5, 10, 30, 60, 120, 1200, 3600},
		},
		[]string{"handler", "method"},
	)

	httpRequestSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_size_bytes",
			Help:    "A histogram of requests sizes for requests.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 32),
		},
		[]string{"handler"},
	)

	httpResponseSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_response_size_bytes",
			Help:    "A histogram of response sizes for requests.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 32),
		},
		[]string{"handler"},
	)
)

func instrumentHandler(name string, handler http.Handler) http.Handler {
	l := prometheus.Labels{"handler": name}
	return promhttp.InstrumentHandlerInFlight(httpInFlightGauge.With(l),
		promhttp.InstrumentHandlerDuration(httpDuration.MustCurryWith(l),
			promhttp.InstrumentHandlerCounter(httpCounter.MustCurryWith(l),
				promhttp.InstrumentHandlerRequestSize(httpRequestSize.MustCurryWith(l),
					promhttp.InstrumentHandlerResponseSize(httpResponseSize.MustCurryWith(l),
						loggingHTTPInterceptor(name, handler),
					),
				),
			),
		),
	)
}

// listenAndSereve starts the server and runs until stopped.
func listenAndServe(stopCh chan string) error {
	wantGrpc := len(serviceHooks) > 0

	debugListener, err := net.Listen("tcp", listenOpts.DebugAddress)
	if err != nil {
		return fmt.Errorf("listen on debug port: %w", err)
	}
	defer debugListener.Close()

	var grpcListener net.Listener
	if wantGrpc {
		var err error
		if addr := listenOpts.GRPCAddress; strings.HasPrefix(addr, "unix:") {
			grpcListener, err = net.Listen("unix", addr[len("unix:"):])
		} else {
			grpcListener, err = net.Listen("tcp", listenOpts.GRPCAddress)
		}
		if err != nil {
			return fmt.Errorf("listen on grpc port: %w", err)
		}
		defer grpcListener.Close()
	}

	var httpListener net.Listener
	if httpHandler != nil {
		var err error
		httpListener, err = net.Listen("tcp", listenOpts.HTTPAddress)
		if err != nil {
			return fmt.Errorf("listen on http port: %w", err)
		}
		defer httpListener.Close()
	}

	doneCh := make(chan error)

	debugServer := &http.Server{
		Handler:  h2c.NewHandler(nethttp.Middleware(opentracing.GlobalTracer(), instrumentHandler("debug_http", http.DefaultServeMux), nethttp.MWSpanFilter(isNotMonitoring), nethttp.MWComponentName("debug_http")), &http2.Server{}),
		ErrorLog: zap.NewStdLog(zap.L().Named("debug_http")),
	}

	var httpServer *http.Server
	if httpHandler != nil {
		// I'd rather blow up with a null pointer dereference than serve the debug mux on
		// the main port, which is what happens if httpHandler is nil.
		httpServer = &http.Server{
			Handler:  h2c.NewHandler(nethttp.Middleware(opentracing.GlobalTracer(), instrumentHandler("http", httpHandler), nethttp.MWComponentName("http")), &http2.Server{}),
			ErrorLog: zap.NewStdLog(zap.L().Named("http")),
		}
	}

	var grpcServer *grpc.Server
	if wantGrpc {
		interceptorCtx := &InterceptorContext{
			Tracer: opentracing.GlobalTracer(),
			Logger: zap.L(),
		}
		var opts []grpc.ServerOption
		var unaryInterceptors []grpc.UnaryServerInterceptor
		for _, f := range unaryInterceptorFns {
			unaryInterceptors = append(unaryInterceptors, f(interceptorCtx))
		}
		if len(unaryInterceptors) > 0 {
			opts = append(opts, grpc.ChainUnaryInterceptor(unaryInterceptors...))
		}
		var streamInterceptors []grpc.StreamServerInterceptor
		for _, f := range streamInterceptorFns {
			streamInterceptors = append(streamInterceptors, f(interceptorCtx))
		}
		if len(streamInterceptors) > 0 {
			opts = append(opts, grpc.ChainStreamInterceptor(streamInterceptors...))
		}
		grpcServer = grpc.NewServer(opts...)
		grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
		channelz.RegisterChannelzServiceToServer(grpcServer)
		for _, h := range serviceHooks {
			if h != nil {
				h(grpcServer)
			}
		}
		reflection.Register(grpcServer)
		defer grpcServer.Stop()
	}

	var servers int
	servers++
	go func() {
		zap.L().Info("listening", zap.String("server", "debug"), zap.String("addr", debugListener.Addr().String()))
		if err := debugServer.Serve(debugListener); err != nil {
			doneCh <- fmt.Errorf("debug server: %v", err)
		} else {
			doneCh <- nil
		}
	}()
	if grpcServer != nil && grpcListener != nil {
		servers++
		go func() {
			zap.L().Info("listening", zap.String("server", "grpc"), zap.String("addr", grpcListener.Addr().String()))
			if err := grpcServer.Serve(grpcListener); err != nil {
				doneCh <- fmt.Errorf("grpc server: %v", err)
			} else {
				doneCh <- nil
			}
		}()
	}
	if httpHandler != nil && httpListener != nil {
		servers++
		go func() {
			zap.L().Info("listening", zap.String("server", "http"), zap.String("addr", httpListener.Addr().String()))
			if err := httpServer.Serve(httpListener); err != nil {
				doneCh <- fmt.Errorf("http server: %v", err)
			} else {
				doneCh <- nil
			}
		}()
	}

	if startupCallback != nil {
		info := Info{
			DebugAddress: debugListener.Addr().String(),
		}
		if grpcListener != nil {
			info.GRPCAddress = grpcListener.Addr().String()
		}
		if httpListener != nil {
			info.HTTPAddress = httpListener.Addr().String()
		}
		go startupCallback(info)
	}

	select {
	case reason := <-stopCh:
		zap.L().Info("shutdown requested", zap.String("reason", reason), zap.Int("servers_remaining", servers))
	case doneErr := <-doneCh:
		servers--
		zap.L().Error("server unexpectedly exited", zap.Error(doneErr), zap.Int("servers_remaining", servers))
	}

	zap.L().Info("marking server unhealthy")
	healthServer.Shutdown() // nolint
	if t := listenOpts.PreDrainGracePeriod; t > 0 {
		// The idea here is that when we are sent the termination signal, our service
		// controller also removes you from its list of healthy backends, and so we stop
		// receiving new requests.  However, this does not always happen instantly.  This
		// delay allows us to serve normal traffic for a period of time after we have been
		// removed.  Eventually traffic volume will trail off as service discovery stops
		// being able to discover us.  Then we stop accepting connections entirely and allow
		// existing connections to finish (the graceful drain), and finally shut down.
		zap.L().Info("sleeping before drain to wait for network reprogramming", zap.Duration("duration", t))
		time.Sleep(t)
	}

	zap.L().Info("beginning graceful drain", zap.Duration("duration", listenOpts.ShutdownGracePeriod))
	tctx, c := context.WithTimeout(context.Background(), listenOpts.ShutdownGracePeriod)
	defer c()
	for _, h := range drainHandlers {
		h()
	}

	go debugServer.Shutdown(tctx) // nolint
	if grpcServer != nil {
		go grpcServer.GracefulStop() // nolint
	}
	if httpServer != nil {
		go httpServer.Shutdown(tctx) // nolint
	}

	for servers > 0 {
		select {
		case <-tctx.Done():
			zap.L().Error("context expired during graceful drain", zap.Error(err), zap.Int("servers_remaining", servers))
			return fmt.Errorf("context expired during graceful drain: %w", tctx.Err())
		case err := <-doneCh:
			servers--
			zap.L().Info("server exited during graceful drain", zap.Error(err), zap.Int("servers_remaining", servers))
		}
	}
	zap.L().Info("all servers exited")
	return nil
}

var terminationLog = "/dev/termination-log"

// ListenAndServe starts all servers.  SIGTERM or SIGINT will gracefully drain connections.  When
// all servers have exited, this returns.  It is not safe to call ListenAndServe again.
func ListenAndServe() {
	stopCh := make(chan string)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		name := sig.String()
		zap.L().Info("got signal", zap.String("signal", name))
		signal.Stop(sigCh)
		stopCh <- name
	}()

	termMsg := []byte("clean shutdown")
	err := listenAndServe(stopCh)
	signal.Stop(sigCh)
	if err != nil {
		zap.L().Info("server errored", zap.Error(err))
		termMsg = []byte(fmt.Sprintf("error during shutdown: %v", err))
	}

	if err := os.WriteFile(terminationLog, termMsg, 0666); err != nil {
		zap.L().Info("failed to write termination log", zap.String("path", terminationLog), zap.Error(err))
	}

	if flushTraces != nil {
		flushTraces.Close()
	}
	zap.L().Sync()
	restoreLogger() // This is why we don't allow ListenAndServe to run twice.  It could probably be fixed.
}
