package grpcserver

import (
	"context"
	"errors"

	"backend-go/internal/kernel/apperr"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CodeOf maps an error Kind to a gRPC code (the counterpart of respond.StatusOf).
func CodeOf(k apperr.Kind) codes.Code {
	switch k {
	case apperr.KindInvalid:
		return codes.InvalidArgument
	case apperr.KindNotFound:
		return codes.NotFound
	case apperr.KindConflict:
		return codes.AlreadyExists
	case apperr.KindFailedPrecondition:
		return codes.FailedPrecondition
	case apperr.KindUnauthenticated:
		return codes.Unauthenticated
	case apperr.KindForbidden:
		return codes.PermissionDenied
	case apperr.KindRateLimited:
		return codes.ResourceExhausted
	case apperr.KindUnavailable:
		return codes.Unavailable
	case apperr.KindTimeout:
		return codes.DeadlineExceeded
	default:
		return codes.Internal
	}
}

// Error converts any error into a gRPC status error using the shared taxonomy.
// Existing status errors pass through; unclassified errors become a generic Internal.
func Error(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	}
	kind := apperr.KindOf(err)
	return status.Error(CodeOf(kind), apperr.PublicMessage(err))
}
