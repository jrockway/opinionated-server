// Executable "example" shows what it looks like to use opinionated-server.
package main

import (
	"net/http"

	"github.com/jrockway/opinionated-server/server"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type flags struct {
	Message string `long:"message" short:"m" env:"MESSAGE" default:"Hello, world!" description:"A message to log on startup."`
}

func main() {
	// Set our name for tracing.
	server.AppName = "example"

	// Add our own flags.
	f := new(flags)
	server.AddFlagGroup("Example", f)

	// Add a "production" http handler.
	server.SetHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(f.Message))
	}))

	// Print a message when the servers are started.  The library does already do this, but this
	// hook exists if you need it.
	server.SetStartupCallback(func(_ server.Info) {
		zap.L().Info("servers are started!", zap.String("message", f.Message))
	})

	// Add a gRPC server.  (Commented-out to avoid adding a dependency on protocol buffers to
	// this project.)
	server.AddService(func(s *grpc.Server) {
		// nolint
		// myproto.RegisterMyServer(s, &myImpl)
	})

	// Initialize the loggers, tracers, etc.
	server.Setup()

	// Run until someone kills us.  SIGINT and SIGTERM exit after draining connections.
	server.ListenAndServe()
}
