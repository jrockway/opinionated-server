package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/golang/protobuf/jsonpb"
	"github.com/golang/protobuf/proto"
	"github.com/grpc-ecosystem/go-grpc-middleware/logging/zap/ctxzap"
	jaegerzap "github.com/uber/jaeger-client-go/log/zap"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// This package logs RPC requests to zap.  Obviously go-grpc-middleware/logging/zap does this, but
// not as well.
var (
	jsonMarshal = &jsonpb.Marshaler{EmitDefaults: true}
)

// pbw exists to do a better job of marshaling protos to JSON than zap.Reflect does.  It supports
// google protos and gogo protos because some programs use both.  How fun!
//
// If you are using something like https://github.com/kazegusuri/go-proto-zap-marshaler, we will use
// those generated methods instead.
type pbw struct {
	msg interface{}
}

// MarshalLogObject implements zapcore.ObjectMarshaler.
func (p *pbw) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	if p == nil {
		return errors.New("nil pb wrapper")
	}
	switch msg := p.msg.(type) {
	case proto.Message:
		if text, err := jsonMarshal.MarshalToString(msg); err != nil {
			return fmt.Errorf("marshal google proto json: %w", err)
		} else {
			enc.AddString("msg", text)
		}
	case zapcore.ObjectMarshaler:
		enc.AddObject("msg", msg)
	default:
		enc.AddReflected("msg", msg)
	}
	return nil
}

// mdw marshals grpc metadata and http headers.
type mdw struct {
	md map[string][]string
}

// MarshalLogArray implements zapcore.ArrayMarshaler.
func (m *mdw) MarshalLogArray(enc zapcore.ArrayEncoder) error {
	if m == nil {
		return errors.New("nil metadata.MD wrapper")
	}
	if m.md == nil {
		return errors.New("nil metadata.MD in wrapper")
	}

	var keys []string
	for k := range m.md {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		for _, v := range m.md[k] {
			enc.AppendString(fmt.Sprintf("%s=%s", k, v))
		}
	}
	return nil
}

func shouldLog(method string) bool {
	return !suppressInstrumentation(method)
}

func loggerFor(ctx context.Context, fullMethod string) (context.Context, *zap.Logger) {
	service := path.Dir(fullMethod)
	method := path.Base(fullMethod)

	var commonFields []zap.Field
	commonFields = append(commonFields, zap.String("grpc.service", service), zap.String("grpc.method", method), jaegerzap.Trace(ctx))
	if d, ok := ctx.Deadline(); ok {
		commonFields = append(commonFields, zap.Time("grpc.deadline", d))
	}
	l := zap.L().Named(method).With(commonFields...)
	return ctxzap.ToContext(ctx, l), l
}

func logStart(ctx context.Context, l *zap.Logger, method string, req interface{}) {
	if !shouldLog(method) {
		return
	}
	var reqFields []zap.Field
	if md, ok := metadata.FromIncomingContext(ctx); ok && logOpts.LogMetadata {
		reqFields = append(reqFields, zap.Array("grpc.metadata", &mdw{md}))
	}
	if logOpts.LogPayloads && req != nil {
		reqFields = append(reqFields, zap.Object("grpc.request", &pbw{req}))
	}
	l.With(reqFields...).Debug("grpc call started")
}

func logEnd(ctx context.Context, method string, start time.Time, trailers metadata.MD, res interface{}, err error) {
	fields := []zap.Field{
		zap.Error(err),
		zap.String("grpc.code", status.Code(err).String()),
		zap.Duration("grpc.duration", time.Since(start)),
	}
	if logOpts.LogMetadata && trailers != nil {
		fields = append(fields, zap.Array("grpc.trailers", &mdw{trailers}))
	}
	if logOpts.LogPayloads && res != nil {
		fields = append(fields, zap.Object("grpc.response", &pbw{res}))
	}
	resLogger := ctxzap.Extract(ctx).With(fields...)
	if err != nil {
		// Skip stacktrace here, since it's just to this point and not to the RPC that blew
		// up.
		ce := resLogger.Check(zap.ErrorLevel, "grpc call finished with error")
		ce.Entry.Stack = ""
		ce.Write()
	} else if shouldLog(method) {
		resLogger.Debug("grpc call finished")
	}
}

func loggingUnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		ctx, logger := loggerFor(ctx, info.FullMethod)
		logStart(ctx, logger, info.FullMethod, req)
		start := time.Now()
		res, err := handler(ctx, req)

		logEnd(ctx, info.FullMethod, start, nil, res, err)
		return res, err
	}
}

func loggingHttpInterceptor(name string, handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		logger := zap.L().Named(name).With(zap.String("uri", req.URL.String()), jaegerzap.Trace(ctx))

		logctx := ctxzap.ToContext(ctx, logger)
		req = req.WithContext(logctx)

		if isNotMonitoring(req) {
			reqLogger := logger
			if logOpts.LogMetadata {
				reqLogger = logger.With(zap.Array("headers", &mdw{req.Header}))
			}
			reqLogger.Debug("http request")
		}

		handler.ServeHTTP(w, req)
		// TODO(jrockway): wrap the requestwriter to print the status here
	})
}

// wrap the server stream to log send/recv events, capture the trailers, and let the rpc method
// get at the logging context.
type wrappedServerStream struct {
	stream          grpc.ServerStream
	ctx             context.Context
	shouldLog       bool
	l               *zap.Logger
	hMu, tMu        sync.Mutex
	header, trailer metadata.MD
}

// Context implements grpc.ServerStream.
func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

// SetHeader implements grpc.ServerStream.
func (w *wrappedServerStream) SetHeader(md metadata.MD) error {
	if w.shouldLog && logOpts.LogMetadata {
		w.hMu.Lock()
		w.header = metadata.Join(w.header, md)
		w.hMu.Unlock()
	}
	return w.stream.SetHeader(md)
}

// SendHeader implements grpc.ServerStream.
func (w *wrappedServerStream) SendHeader(md metadata.MD) error {
	if w.shouldLog && logOpts.LogMetadata {
		w.hMu.Lock()
		w.header = metadata.Join(w.header, md)
		w.l.Debug("grpc call sending headers", zap.Array("grpc.headers", &mdw{w.header.Copy()}))
		w.header = nil
		w.hMu.Unlock()
	}

	return w.stream.SendHeader(md)
}

// SetTrailer implements grpc.ServerStream.
func (w *wrappedServerStream) SetTrailer(md metadata.MD) {
	if w.shouldLog && logOpts.LogMetadata {
		w.tMu.Lock()
		w.trailer = metadata.Join(w.trailer, md)
		w.tMu.Unlock()
	}
	w.stream.SetTrailer(md)
}

// RecvMsg implements grpc.ServerStream.
func (w *wrappedServerStream) RecvMsg(m interface{}) error {
	err := w.stream.RecvMsg(m)
	if w.shouldLog && logOpts.LogPayloads && err == nil {
		w.l.Debug("grpc call received message", zap.Object("grpc.incoming_msg", &pbw{m}))
	} else if w.shouldLog && err != nil {
		w.l.Error("grpc receive message failed", zap.Error(err))
	}
	return err

}

// SendMsg implements grpc.ServerStream.
func (w *wrappedServerStream) SendMsg(m interface{}) error {
	if w.shouldLog && logOpts.LogPayloads {
		w.l.Debug("grpc call sent message", zap.Object("grpc.outgoing_msg", &pbw{m}))
	}
	return w.stream.SendMsg(m)
}

func loggingStreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, logger := loggerFor(stream.Context(), info.FullMethod)
		logStart(ctx, logger, info.FullMethod, nil)

		wrapped := &wrappedServerStream{stream: stream, ctx: ctx, l: logger.WithOptions(zap.AddCallerSkip(1)), shouldLog: shouldLog(info.FullMethod)}
		start := time.Now()
		err := handler(srv, wrapped)
		logEnd(ctx, info.FullMethod, start, wrapped.trailer, nil, err)
		return err
	}
}
