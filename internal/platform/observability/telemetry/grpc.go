package telemetry

import (
	"context"

	"backend-go/internal/platform/observability/requestid"

	"github.com/google/uuid"
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
		} else {
			sc = sc.NewChildSpan()
		}

		ctx = ContextWithSpan(ctx, sc)
		ctx = withRequestID(ctx, md)
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
		if id := requestid.From(ctx); id != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, requestid.MetadataKey, id)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// withRequestID adopts a well-formed inbound x-request-id or creates one, and
// returns it to the caller as a response header.
func withRequestID(ctx context.Context, md metadata.MD) context.Context {
	id := ""
	if vals := md.Get(requestid.MetadataKey); len(vals) > 0 && requestid.Valid(vals[0]) {
		id = vals[0]
	}
	if id == "" {
		id = uuid.NewString()
	}
	_ = grpc.SetHeader(ctx, metadata.Pairs(requestid.MetadataKey, id))
	return requestid.With(ctx, id)
}

// StreamServerTraceInterceptor is the streaming counterpart of UnaryServerTraceInterceptor.
func StreamServerTraceInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()
		md, _ := metadata.FromIncomingContext(ctx)
		sc := NewSpanContext()
		if vals := md.Get(HeaderTraceParent); len(vals) > 0 {
			if parsed, ok := ParseTraceparent(vals[0]); ok {
				sc = parsed.NewChildSpan()
			}
		}
		ctx = withRequestID(ContextWithSpan(ctx, sc), md)
		return handler(srv, &tracedStream{ServerStream: ss, ctx: ctx})
	}
}

type tracedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (t *tracedStream) Context() context.Context { return t.ctx }
