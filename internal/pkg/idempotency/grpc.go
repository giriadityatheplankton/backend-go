package idempotency

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// GRPCConfig configures the unary idempotency interceptor.
type GRPCConfig struct {
	Store Store
	// Scope returns the principal/tenant namespace from the auth context.
	Scope func(ctx context.Context) string
	// Required reports whether fullMethod must carry a key. Nil means "optional everywhere".
	Required     func(fullMethod string) bool
	FailureMode  FailureMode
	InFlightTTL  time.Duration
	CompletedTTL time.Duration
	Observer     Observer
}

// deterministicCodes are business outcomes that will repeat for the same request,
// so they are safe to replay. Everything else (Unavailable, Internal,
// DeadlineExceeded, ResourceExhausted, Unauthenticated, ...) releases the key.
func deterministic(c codes.Code) bool {
	switch c {
	case codes.InvalidArgument, codes.NotFound, codes.FailedPrecondition, codes.AlreadyExists, codes.OutOfRange:
		return true
	}
	return false
}

// UnaryServerInterceptor returns an interceptor implementing edge idempotency.
// Success responses are stored as anypb.Any so the replay has the exact
// response type; deterministic errors are stored as code + message.
func UnaryServerInterceptor(cfg GRPCConfig) grpc.UnaryServerInterceptor {
	if cfg.InFlightTTL <= 0 {
		cfg.InFlightTTL = DefaultInFlightTTL
	}
	if cfg.CompletedTTL <= 0 {
		cfg.CompletedTTL = DefaultCompletedTTL
	}
	if cfg.Scope == nil {
		cfg.Scope = func(context.Context) string { return "anonymous" }
	}
	marshal := proto.MarshalOptions{Deterministic: true}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		key := keyFromMetadata(ctx)
		if key == "" {
			if cfg.Required != nil && cfg.Required(info.FullMethod) {
				return nil, status.Error(codes.InvalidArgument, "idempotency-key metadata is required")
			}
			return handler(ctx, req)
		}
		if !ValidKey(key) {
			return nil, status.Error(codes.InvalidArgument, "invalid idempotency-key")
		}

		ctx = WithKey(ctx, key)
		reqMsg, ok := req.(proto.Message)
		if !ok {
			return handler(ctx, req) // not a protobuf call; cannot fingerprint safely
		}
		payload, err := marshal.Marshal(reqMsg)
		if err != nil {
			return nil, status.Error(codes.Internal, "idempotency: cannot fingerprint request")
		}
		fingerprint := Fingerprint(info.FullMethod, string(payload))
		storeKey := ScopedKey(cfg.Scope(ctx), "GRPC", info.FullMethod, key)

		lease, existing, err := cfg.Store.Acquire(ctx, storeKey, fingerprint, cfg.InFlightTTL)
		if err != nil {
			observe(cfg.Observer, ResultStoreError)
			slog.ErrorContext(ctx, "Idempotency store unavailable", "error", err, "mode", cfg.FailureMode)
			if cfg.FailureMode == FailOpen {
				observe(cfg.Observer, ResultBypassed)
				return handler(ctx, req)
			}
			return nil, status.Error(codes.Unavailable, "idempotency service unavailable")
		}

		if existing != nil {
			if existing.Fingerprint != "" && existing.Fingerprint != fingerprint {
				observe(cfg.Observer, ResultMismatch)
				return nil, status.Error(codes.InvalidArgument, "idempotency-key was already used with a different request")
			}
			if existing.State != StateCompleted {
				observe(cfg.Observer, ResultInProgress)
				return nil, status.Error(codes.Aborted, "a request with this idempotency-key is still being processed")
			}
			observe(cfg.Observer, ResultReplayed)
			_ = grpc.SetHeader(ctx, metadata.Pairs("idempotent-replayed", "true"))
			if existing.GRPCCode != 0 {
				return nil, status.Error(codes.Code(existing.GRPCCode), existing.GRPCMessage)
			}
			var a anypb.Any
			if err := proto.Unmarshal(existing.Body, &a); err != nil {
				return nil, status.Error(codes.Internal, "idempotency: corrupt stored response")
			}
			msg, err := a.UnmarshalNew() // concrete RESPONSE type, resolved from the type URL
			if err != nil {
				return nil, status.Error(codes.Internal, "idempotency: stored response type is unknown")
			}
			return msg, nil
		}

		observe(cfg.Observer, ResultAcquired)
		stop := heartbeat(ctx, cfg.Store, lease, cfg.InFlightTTL, cfg.Observer)
		completed := false
		defer func() {
			stop()
			if completed {
				return
			}
			rctx, cancel := persistContext(ctx)
			defer cancel()
			if err := cfg.Store.Release(rctx, lease); err != nil && !errors.Is(err, ErrLeaseLost) {
				slog.ErrorContext(ctx, "Failed to release idempotency lease", "error", err)
			} else {
				observe(cfg.Observer, ResultReleased)
			}
		}()

		res, herr := handler(ctx, req)

		out := &Record{Fingerprint: fingerprint}
		switch { //nolint:staticcheck // boolean cases read better than a tagged switch here
		case herr == nil:
			respMsg, ok := res.(proto.Message)
			if !ok {
				return res, herr
			}
			packed, err := anypb.New(respMsg)
			if err != nil {
				return res, herr
			}
			if out.Body, err = proto.Marshal(packed); err != nil {
				return res, herr
			}
		default:
			st, _ := status.FromError(herr)
			if !deterministic(st.Code()) {
				return res, herr // deferred release
			}
			out.GRPCCode = uint32(st.Code())
			out.GRPCMessage = st.Message()
		}

		stop()
		saveCtx, cancel := persistContext(ctx)
		defer cancel()
		switch err := cfg.Store.Complete(saveCtx, lease, out, cfg.CompletedTTL); {
		case err == nil:
			completed = true
		case errors.Is(err, ErrLeaseLost):
			completed = true
			observe(cfg.Observer, ResultLeaseLost)
			slog.ErrorContext(ctx, "Idempotency lease lost before result was saved", "key", lease.Key)
		default:
			slog.ErrorContext(ctx, "Failed to save idempotent gRPC response", "error", err)
		}
		return res, herr
	}
}

func keyFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, k := range []string{MetadataIdempotencyKey, MetadataLegacyIdempotencyKey} {
		if v := md.Get(k); len(v) > 0 && strings.TrimSpace(v[0]) != "" {
			return strings.TrimSpace(v[0])
		}
	}
	return ""
}

// GRPCKey returns the client's idempotency key for a gRPC call: the value stored
// by the interceptor, or the raw metadata when the interceptor is not installed.
func GRPCKey(ctx context.Context) string {
	if k := KeyFrom(ctx); k != "" {
		return k
	}
	return keyFromMetadata(ctx)
}
