//go:build integration && examples

package integration_test

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func grpcHeader(md *metadata.MD) grpc.CallOption { return grpc.Header(md) }
