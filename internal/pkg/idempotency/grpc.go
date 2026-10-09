package idempotency

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	GRPCMetadataIdempotencyKey = "x-idempotency-key"
)

// UnaryServerInterceptor creates standard gRPC UnaryServerInterceptor for idempotency.
func UnaryServerInterceptor(storage Storage, inFlightTTL, completedTTL time.Duration) grpc.UnaryServerInterceptor {
	if inFlightTTL <= 0 {
		inFlightTTL = DefaultInFlightTTL
	}
	if completedTTL <= 0 {
		completedTTL = DefaultCompletedTTL
	}

	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return handler(ctx, req)
		}

		var idempotencyKey string
		if vals := md.Get(GRPCMetadataIdempotencyKey); len(vals) > 0 {
			idempotencyKey = vals[0]
		} else if vals := md.Get(strings.TrimPrefix(GRPCMetadataIdempotencyKey, "x-")); len(vals) > 0 {
			idempotencyKey = vals[0]
		}

		if idempotencyKey == "" {
			return handler(ctx, req)
		}

		// 1. Try acquire lock
		acquired, existing, err := storage.TryLock(ctx, idempotencyKey, inFlightTTL)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "idempotency verification failed: %v", err)
		}

		// 2. Handle existing record
		if !acquired && existing != nil {
			if existing.Status == StatusStarted {
				return nil, status.Error(codes.Aborted, "concurrent request with this idempotency key is already in progress")
			}

			if existing.Status == StatusCompleted {
				// If handler response is protobuf message, try to unmarshal
				if protoMsg, ok := req.(proto.Message); ok {
					replyMsg := protoMsg.ProtoReflect().New().Interface()
					if err := protojson.Unmarshal(existing.ResponseBody, replyMsg); err == nil {
						return replyMsg, nil
					}
				}
				// Generic JSON unmarshal fallback
				var genericResp map[string]interface{}
				if err := json.Unmarshal(existing.ResponseBody, &genericResp); err == nil {
					return genericResp, nil
				}
			}
		}

		// 3. Execute handler
		res, err := handler(ctx, req)
		if err != nil {
			_ = storage.ReleaseLock(ctx, idempotencyKey)
			return nil, err
		}

		// 4. Save response
		var payload []byte
		if protoMsg, ok := res.(proto.Message); ok {
			payload, _ = protojson.Marshal(protoMsg)
		} else {
			payload, _ = json.Marshal(res)
		}

		_ = storage.SaveResponse(ctx, idempotencyKey, &Record{
			Key:          idempotencyKey,
			Status:       StatusCompleted,
			ResponseBody: payload,
			CreatedAt:    time.Now(),
		}, completedTTL)

		return res, nil
	}
}
