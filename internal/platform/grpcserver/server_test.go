package grpcserver_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"backend-go/internal/kernel/apperr"
	"backend-go/internal/platform/grpcserver"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestPortConflictFailsFast(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer busy.Close()
	srv, err := grpcserver.New(grpcserver.Config{Address: busy.Addr().String()}, nil, nil)
	require.NoError(t, err)
	assert.Error(t, srv.Listen(), "a busy port must be a startup error, not a silent no-op")
}

func TestHealthFollowsReadinessAndShutdownIsGraceful(t *testing.T) {
	srv, err := grpcserver.New(grpcserver.Config{Address: "127.0.0.1:0", ShutdownTimeout: time.Second}, nil, nil)
	require.NoError(t, err)
	require.NoError(t, srv.Listen())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	hc := healthpb.NewHealthClient(conn)

	require.Eventually(t, func() bool {
		r, err := hc.Check(context.Background(), &healthpb.HealthCheckRequest{})
		return err == nil && r.Status == healthpb.HealthCheckResponse_SERVING
	}, 2*time.Second, 20*time.Millisecond)

	srv.SetServing(false)
	r, err := hc.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_NOT_SERVING, r.Status)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestErrorMapping(t *testing.T) {
	cases := map[string]struct {
		err  error
		code codes.Code
		msg  string
	}{
		"not found":    {apperr.New(apperr.KindNotFound, "X", "gone"), codes.NotFound, "gone"},
		"conflict":     {apperr.New(apperr.KindConflict, "X", "dup"), codes.AlreadyExists, "dup"},
		"precond":      {apperr.New(apperr.KindFailedPrecondition, "X", "no"), codes.FailedPrecondition, "no"},
		"wrapped":      {errors.Join(errors.New("ctx"), apperr.New(apperr.KindInvalid, "X", "bad")), codes.InvalidArgument, "bad"},
		"unclassified": {errors.New("pq: password authentication failed"), codes.Internal, "An unexpected error occurred"},
		"deadline":     {context.DeadlineExceeded, codes.DeadlineExceeded, "deadline exceeded"},
		"passthrough":  {status.Error(codes.PermissionDenied, "nope"), codes.PermissionDenied, "nope"},
	}
	for name, c := range cases {
		got := grpcserver.Error(c.err)
		assert.Equal(t, c.code, status.Code(got), name)
		assert.Equal(t, c.msg, status.Convert(got).Message(), name)
	}
	assert.NoError(t, grpcserver.Error(nil))
}
