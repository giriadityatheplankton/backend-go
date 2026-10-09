package telemetry

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// UnaryServerTraceInterceptor extracts incoming traceparent from gRPC metadata.
func UnaryServerTraceInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		var sc SpanContext
		if ok {
			vals := md.Get(HeaderTraceParent)
			if len(vals) > 0 {
				if parsed, ok := ParseTraceparent(vals[0]); ok {
					sc = parsed
				}
			}
		}

		if sc.TraceID == "" {
			sc = NewSpanContext()
		}

		ctx = ContextWithSpan(ctx, sc)
		return handler(ctx, req)
	}
}

// UnaryClientTraceInterceptor injects traceparent into outgoing gRPC metadata.
func UnaryClientTraceInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		if sc, ok := SpanFromContext(ctx); ok {
			ctx = metadata.AppendToOutgoingContext(ctx, HeaderTraceParent, sc.Traceparent())
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
