# opinionated-server

Every time I need a quick gRPC or HTTP server I end up spending three hours hooking in all the
little additions I want. This package represents my opinion on what this server should always look
like. If you agree with my opinions, you can save yourself those three hours.

For a quick start, take a look at an [example server](https://github.com/jrockway/opinionated-server/blob/master/example/main.go).

## What we add

We use [zap](https://github.com/uber-go/zap) for structured logging. It is arranged for standard
`log` calls to go to zap. You can also use `zap.L()` anywhere in your program to produce structured
logs. The debug handler, described below, allows you to change the log level at runtime over HTTP.

We use [Jaeger](https://www.jaegertracing.io/) for distributed tracing. All gRPC and HTTP calls are
automatically traced. We use [B3 propagation](https://github.com/openzipkin/b3-propagation) for
easier interoperability with Zipkin (`x-b3-trace-id`). When the W3C Trace Context standard is
finalized, we will switch to that. You can configure Jaeger with the
[standard environment variables](https://www.jaegertracing.io/docs/1.16/client-features/).

We use [Prometheus](https://prometheus.io/) for monitoring. HTTP and gRPC handlers are already
instrumented. You can use `promauto.` to add additional metrics.

We use [go-flags](https://github.com/jessevdk/go-flags) to read flags and environment variables. We
have some configuration of our own, but you can add as many "groups" to the flags parser as you
want, for your own configuration. In general, all configuration of this package itself can be done
through either flags or environment variables. I like flags for playing with things on the command
line, and environment variables for production. I recommend supporting both; go-flags makes this
straightforward.

## The servers

We start three servers. They are gracefully drained when SIGTERM or SIGINT is received. (So clients
with requests in progress should not see connection resets when Kubernetes shuts down your pod, for
example. Of course, your server can still randomly die at any time, so you should prepare yourself
for aborted requests nonetheless.)

### gRPC

gRPC will be served even if you don't add any additional handlers with `server.AddService`. We provide the standard
[Health Check](https://github.com/grpc/grpc/blob/master/doc/health-checking.md) service (compatible with
[Envoy](https://www.envoyproxy.io/docs/envoy/latest/api-v2/api/v2/core/health_check.proto#envoy-api-msg-core-healthcheck-grpchealthcheck)
and [Kubernetes](https://github.com/grpc-ecosystem/grpc-health-probe/)), the [channelz](https://grpc.io/blog/a_short_introduction_to_channelz/) service, and the Discovery service
(for use with `grpc_cli` or [grpcurl](https://github.com/fullstorydev/grpcurl)).

### Debug

A debug server serves the default serve mux (to avoid exposing internal resources unintentionally). It contains:

-   `/metrics` for Prometheus to scrape. By default, you get Go statistics (memory use, goroutine count, etc.), detailed gRPC statistics, detailed HTTP statistics, and Jaeger statistics.
-   `/zap` for adjusting the zap log level. (See [the godoc](https://godoc.org/go.uber.org/zap#AtomicLevel.ServeHTTP) for details. It is in the scope of this project to have a utility that automatically changes the log level, but it's not here yet.)
-   `/debug/pprof` for [standard go profiling](https://golang.org/pkg/net/http/pprof/).

You can add your own health handler (`http.Handle('/healthz', ...)`) if you don't want to use the
gRPC health checking.

### HTTP

If you add an HTTP handler to the server with `server.SetHTTPHandler`, an additional port will be
bound to serve this handler. To serve multiple "pages", use an `http.ServeMux`.

## Extras

When shutting down, we attempt to write our status to `/dev/termination-log`. A spurious message
will be logged when that's not writable; if you aren't on Kubernetes, you don't need to worry about
it.

### Configuration

The `config/` directory contains example configurations showing how to integrate the outside world with a server written using this framework.

The `config/grafana/` directory contains example dashboards that take advantage of the metrics we
collect.

Both of these are currently empty, but should be filled in Real Soon.

## Versioning policy

We use semantic versioning. Depending on `master` is likely to break your production environment,
so pick a version explicitly.

Changes that break your consuming Go code or change how your production environment will work will
increase the major version number. Updates that add features will increase the minor version
number. Bug fixes or small tweaks will increase the patch level.

## Contribution policy

This is not meant to be a generic thing that meets all needs. Patches like "use OpenCensus instead
of Jaeger" or "use logrus instead of zap" will not be accepted. It is not worth making someone
decide which of the 8 million possible libraries they should use. I already picked.

Patches that add useful metrics, make it easier to run TLS for local testing, etc. would be greatly
appreciated.
