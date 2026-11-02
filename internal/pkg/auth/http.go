package auth

import (
	"context"
	"net/http"

	"backend-go/internal/pkg/httpserver/respond"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// HTTPMiddleware authenticates every request and stores the Principal in the
// request context. With a nil Authenticator (AUTH_MODE=none) requests pass through
// unauthenticated; the app refuses that mode in production.
func HTTPMiddleware(a Authenticator, skipPaths ...string) gin.HandlerFunc {
	skip := make(map[string]struct{}, len(skipPaths))
	for _, p := range skipPaths {
		skip[p] = struct{}{}
	}
	return func(c *gin.Context) {
		if a == nil {
			c.Next()
			return
		}
		if _, ok := skip[c.Request.URL.Path]; ok {
			c.Next()
			return
		}
		token, err := BearerToken(c.GetHeader("Authorization"))
		if err != nil {
			c.Header("WWW-Authenticate", `Bearer realm="api"`)
			respond.Error(c, http.StatusUnauthorized, ErrUnauthenticated.Code, ErrUnauthenticated.Message)
			c.Abort()
			return
		}
		p, err := a.Authenticate(c.Request.Context(), token)
		if err != nil {
			c.Header("WWW-Authenticate", `Bearer realm="api", error="invalid_token"`)
			respond.Error(c, http.StatusUnauthorized, ErrUnauthenticated.Code, ErrUnauthenticated.Message)
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(With(c.Request.Context(), p))
		c.Next()
	}
}

// RequireScope rejects principals lacking scope. A request without a principal is
// only allowed through when no authenticator is configured (development).
func RequireScope(a Authenticator, scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if a == nil {
			c.Next()
			return
		}
		p, ok := From(c.Request.Context())
		if !ok || !p.HasScope(scope) {
			respond.Error(c, http.StatusForbidden, ErrForbidden.Code, ErrForbidden.Message)
			c.Abort()
			return
		}
		c.Next()
	}
}

// UnaryServerInterceptor authenticates gRPC calls from the "authorization" metadata.
// exempt lists full method names (e.g. health checks) that skip authentication.
func UnaryServerInterceptor(a Authenticator, exempt ...string) grpc.UnaryServerInterceptor {
	skip := make(map[string]struct{}, len(exempt))
	for _, m := range exempt {
		skip[m] = struct{}{}
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := authenticateGRPC(ctx, a, info.FullMethod, skip)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamServerInterceptor is the streaming counterpart of UnaryServerInterceptor.
func StreamServerInterceptor(a Authenticator, exempt ...string) grpc.StreamServerInterceptor {
	skip := make(map[string]struct{}, len(exempt))
	for _, m := range exempt {
		skip[m] = struct{}{}
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := authenticateGRPC(ss.Context(), a, info.FullMethod, skip)
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
	}
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

func authenticateGRPC(ctx context.Context, a Authenticator, method string, skip map[string]struct{}) (context.Context, error) {
	if a == nil {
		return ctx, nil
	}
	if _, ok := skip[method]; ok {
		return ctx, nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return ctx, status.Error(codes.Unauthenticated, ErrUnauthenticated.Message)
	}
	token, err := BearerToken(vals[0])
	if err != nil {
		return ctx, status.Error(codes.Unauthenticated, ErrUnauthenticated.Message)
	}
	p, err := a.Authenticate(ctx, token)
	if err != nil {
		return ctx, status.Error(codes.Unauthenticated, ErrUnauthenticated.Message)
	}
	return With(ctx, p), nil
}
