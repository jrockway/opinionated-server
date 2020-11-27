package server

import (
	"context"
	"reflect"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/metadata"
)

type emptyStream struct{}

func (s *emptyStream) Context() context.Context        { return context.Background() }
func (s *emptyStream) RecvMsg(m interface{}) error     { return nil }
func (s *emptyStream) SendMsg(m interface{}) error     { return nil }
func (s *emptyStream) SendHeader(md metadata.MD) error { return nil }
func (s *emptyStream) SetHeader(md metadata.MD) error  { return nil }
func (s *emptyStream) SetTrailer(md metadata.MD)       {}

type emptyMsg struct{}

func (m *emptyMsg) ProtoMessage()  {}
func (m *emptyMsg) String() string { return "" }
func (m *emptyMsg) Reset()         {}

func TestStreamWrapper(t *testing.T) {
	logOpts.LogMetadata = true
	logOpts.LogPayloads = true
	defer func() { logOpts.LogMetadata = false; logOpts.LogPayloads = false }()
	core, obs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	wrapped := &wrappedServerStream{
		stream:    new(emptyStream),
		l:         logger,
		ctx:       context.Background(),
		shouldLog: true,
	}
	if got, want := obs.Len(), 0; got != want {
		t.Errorf("start: log count:\n  got: %v\n want: %v", got, want)
	}

	wrapped.SetHeader(metadata.Pairs("foo", "bar"))
	if got, want := obs.Len(), 0; got != want {
		t.Errorf("after set header: log count:\n  got: %v\n want: %v", got, want)
	}
	wrapped.SendHeader(metadata.Pairs("bar", "baz"))
	if got, want := obs.Len(), 1; got != want {
		t.Errorf("after send header: log count:\n  got: %v\n want: %v", got, want)
	}
	if got, want := obs.All()[0].ContextMap()["grpc.headers"], []interface{}{"bar=baz", "foo=bar"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after send header: sent headers:\n  got: %#v\n want: %#v", got, want)
	}

	wrapped.SetTrailer(metadata.Pairs("baz", "quux"))
	if got, want := obs.Len(), 1; got != want {
		t.Errorf("after set trailer: log count:\n got: %v\n want: %v", got, want)
	}
	wrapped.RecvMsg(new(emptyMsg))
	if got, want := obs.Len(), 2; got != want {
		t.Errorf("after recvmsg: log count:\n  got: %v\n want: %v", got, want)
	}

	wrapped.SendMsg(new(emptyMsg))
	if got, want := obs.Len(), 3; got != want {
		t.Errorf("after sendmsg: log count:\n  got: %v\n want: %v", got, want)
	}

	if got, want := wrapped.trailer, (metadata.MD{"baz": []string{"quux"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("captured trailers:\n  got: %#v\n want: %#v", got, want)
	}
}
