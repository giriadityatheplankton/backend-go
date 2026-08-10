package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"backend-go/internal/config"
	"backend-go/internal/events"
	"backend-go/internal/handler"
	"backend-go/internal/pkg/middleware"
	"backend-go/internal/repository"
	"backend-go/internal/usecase"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

func main() {
	// 1. Initialize Structured Logger (slog JSON handler)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// 2. Load application configuration
	cfg := config.LoadConfig()

	// 3. Set Gin Mode
	if cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	// 4. Initialize Redis Client (Graceful fallback)
	var redisClient *redis.Client
	if cfg.RedisAddress != "" {
		slog.Info("Connecting to Redis", "address", cfg.RedisAddress)
		redisClient = redis.NewClient(&redis.Options{
			Addr: cfg.RedisAddress,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		if err := redisClient.Ping(ctx).Err(); err != nil {
			slog.Warn("Failed to connect to Redis, running without cache", "error", err)
			redisClient = nil
		} else {
			slog.Info("Successfully connected to Redis")
		}
	}

	// 5. Initialize NATS Connection (Graceful fallback)
	var natsConn *nats.Conn
	if cfg.NatsAddress != "" {
		slog.Info("Connecting to NATS", "address", cfg.NatsAddress)
		var err error
		natsConn, err = nats.Connect(cfg.NatsAddress)
		if err != nil {
			slog.Warn("Failed to connect to NATS, running without event streaming", "error", err)
			natsConn = nil
		} else {
			slog.Info("Successfully connected to NATS")

			// Example: Background subscriber demonstration
			_, _ = natsConn.Subscribe("user.accessed", func(msg *nats.Msg) {
				slog.Info("[NATS Subscriber Demo] Event received", "data", string(msg.Data))
			})
		}
	}

	// 6. Wire Dependencies
	eventPublisher := events.NewNATSEventPublisher(natsConn)
	userRepo := repository.NewUserRepository(redisClient, cfg.CacheTTL)
	userUsecase := usecase.NewUserUsecase(userRepo, eventPublisher)

	// 7. Setup Router & Middleware
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger())

	// 8. Register HTTP Routes
	handler.RegisterUserRoutes(r, userUsecase)

	// 9. Configure HTTP Server
	srv := &http.Server{
		Addr:         cfg.ServerAddress,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	// 10. Start Server in a separate goroutine
	go func() {
		slog.Info("Server is starting", "address", cfg.ServerAddress, "env", cfg.AppEnv)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Failed to start server", "error", err)
			os.Exit(1)
		}
	}()

	// 11. Graceful Shutdown listener
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("Shutdown signal received, shutting down server gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("Server forced to shutdown", "error", err)
	}

	// Close Redis & NATS connections
	if redisClient != nil {
		_ = redisClient.Close()
		slog.Info("Redis connection closed")
	}
	if natsConn != nil {
		natsConn.Close()
		slog.Info("NATS connection closed")
	}

	slog.Info("Server shutdown complete")
}
