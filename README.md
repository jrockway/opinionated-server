# opinionated-server

![CI](https://ci.jrock.us/api/v1/teams/main/pipelines/opinionated-server/jobs/tests/badge)
[![PkgGoDev](https://pkg.go.dev/badge/github.com/jrockway/opinionated-server)](https://pkg.go.dev/github.com/jrockway/opinionated-server)

Every time I need a quick gRPC or HTTP server I end up spending three hours hooking in all the
little additions I want. This package represents my opinion on what this server should always look
like. If you agree with my opinions, you can save yourself those three hours.

For a quick start, take a look at an
[example server](https://github.com/jrockway/opinionated-server/blob/master/example/main.go).

## What we add

We use [zap](https://github.com/uber-go/zap) for structured logging. It is arranged for standard
`log` calls to go to zap. You can also use `zap.L()` anywhere in your program to produce structured
logs. The debug handler, described below, allows you to change the log level at runtime over HTTP.
RPC methods have a method-scoped logger available from
[ctxzap](https://godoc.org/github.com/grpc-ecosystem/go-grpc-middleware/logging/zap/ctxzap).`Extract(ctx)`
for your own logs, and requests, responses and gRPC stream messages are automatically logged at the
debug level. (Errors are logged at the error level.)

We use [Jaeger](https://www.jaegertracing.io/) for distributed tracing. All gRPC and HTTP calls are
automatically traced. We use [B3 propagation](https://github.com/openzipkin/b3-propagation) for
easier interoperability with Zipkin (`x-b3-traceid`). When the W3C Trace Context standard is
finalized, we will switch to that (with a major version bump). You can configure Jaeger with the
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

A gRPC server is started if you add a service with `server.AddService`. We provide the standard
[Health Check](https://github.com/grpc/grpc/blob/master/doc/health-checking.md) service (compatible
with
[Envoy](https://www.envoyproxy.io/docs/envoy/latest/api-v2/api/v2/core/health_check.proto#envoy-api-msg-core-healthcheck-grpchealthcheck)
and [Kubernetes](https://github.com/grpc-ecosystem/grpc-health-probe/)), the
[channelz](https://grpc.io/blog/a_short_introduction_to_channelz/) service, and the Discovery
service (for use with `grpc_cli` or [grpcurl](https://github.com/fullstorydev/grpcurl)).

We redirect grpc's internal logs to a Zap logger. Logs from grpc itself can be identified with a
`{"logger": "grpc","system":"grpc","grpc_log":true}` tag on the message. A side effect of this
redirection is that grpc will log at the same level as everything else, and is no longer controlled
through `$GRPC_GO_LOG_SEVERITY_LEVEL`. If you set your log level to `info`, then you'll get grpc's
`info` logs as well. `$GRPC_GO_LOG_VERBOSITY_LEVEL` is similarly subsumed, but we add the
functionality back in a way that is exactly compatible with programs that do not use
opinionated-server.

For servers that also talk to upstream gRPC servers, there is a standard list of interceptors
available via the `client.GRPCInterceptors()` method. Installing these interceptors when dialing
will allow logging of requests, propagation of traces, and per-client-RPC metrics.

### Debug

A debug server serves the default serve mux (to avoid exposing internal resources unintentionally).
It contains:

-   `/healthz` returns the state of the gRPC health checker over plain HTTP. If the app is running,
    it return HTTP status 200 and the string "SERVING". Otherwise, it will return a 5xx error.
-   `/metrics` for Prometheus to scrape. By default, you get Go statistics (memory use, goroutine
    count, etc.), detailed gRPC statistics, detailed HTTP statistics, and Jaeger statistics.
-   `/zap` for adjusting the zap log level. (See
    [the godoc](https://godoc.org/go.uber.org/zap#AtomicLevel.ServeHTTP) for details. It is in the
    scope of this project to have a utility that automatically changes the log level, given
    something like a named Kubernetes deployment, but it's not here yet.)
-   `/debug/pprof/` for [standard go profiling](https://golang.org/pkg/net/http/pprof/).

You should ensure that when running your server that external traffic cannot reach the debug port,
or that it goes through an authenticating proxy first. Handlers attached to the debug server are not
designed to be secure against untrusted inputs, and can leak information about your server.

### HTTP

If you add an HTTP handler to the server with `server.SetHTTPHandler`, an additional port will be
bound to serve this handler. To serve multiple "pages", use an `http.ServeMux`.

We send internal log messages generated by `net/http` through named zap loggers. The debug server is
called `debug_http` and the main http server is caled `http`.

If your application makes outgoing HTTP calls, there is a RoundTripper in the `client` package that
logs requests, collects per-request metrics, and injects trace headers to that upstream HTTP servers
can participate in distributed traces. Use it like:

```
    httpClient := &http.Client{Transport: client.WrapRoundTripper(http.DefaultTransport)}
    req, err := http.NewRequestWithContext(ctx, "GET", "http://internal-app.whatever.svc.cluster.local", nil)
    ...
    res, err := httpClient.Do(req)
    ...
    res.Body.Close()
```

Many third-party libraries allow you to inject either a RoundTripper or a Client, so that you can
monitor them.

## Extras

We use [automaxprocs](https://github.com/uber-go/automaxprocs) to set `$GOMAXPROCS`. This will help
ensure that your program doesn't get throttled when running with CPU limits. I consider this
relatively experimental because the current k8s wisdom is to never use CPU limits, and thus this
code will never run. (The problem with CPU limits is that they work by allowing you to run until
you've used your quota, then your entire process goes to sleep for the next throttling period. This
results in high latency for requests that arrived towards the end of your throttling period. Setting
`GOMAXPROCS` to your CPU quota will ensure that the Go runtime can't use any extra cores that will
cause you to throttle, reducing the latency implications. The big caveat is that this only works for
integer quotas.)

When shutting down, we attempt to write our status to `/dev/termination-log`. A spurious message
will be logged when that's not writable; if you aren't on Kubernetes, you don't need to worry about
it.

## Versioning policy

We use semantic versioning. Depending on `master` is likely to break your production environment, so
pick a version explicitly.

Changes that break your consuming Go code or change how your production environment will work will
increase the major version number. Updates that add features will increase the minor version number.
Bug fixes or small tweaks will increase the patch level.

In the v0.0.X phase, nothing is guaranteed. Commits that are known to cause breaking changes should
be marked with "BREAKING CHANGE" or similar, but they will happen frequently.

## Contribution policy

This is not meant to be a generic thing that meets all needs. Patches like "use OpenCensus instead
of Jaeger" or "use logrus instead of zap" will not be accepted. It is not worth making someone
decide which of the 8 million possible libraries they should use. I already picked.

Patches that add useful metrics, make it easier to run TLS for local testing, etc. would be greatly
appreciated.

```

```
