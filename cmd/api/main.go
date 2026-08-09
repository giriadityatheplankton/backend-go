package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"backend-go/internal/config"
	"backend-go/internal/handler"
	"backend-go/internal/repository"
	"backend-go/internal/usecase"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

func main() {
	// 1. Load application configuration securely
	cfg := config.LoadConfig()

	// 2. Set Gin mode based on the environment
	if cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	// 3. Connect to Redis (Graceful / optional connection)
	var redisClient *redis.Client
	if cfg.RedisAddress != "" {
		log.Printf("Connecting to Redis at %s...", cfg.RedisAddress)
		redisClient = redis.NewClient(&redis.Options{
			Addr: cfg.RedisAddress,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		if err := redisClient.Ping(ctx).Err(); err != nil {
			log.Printf("Warning: Failed to connect to Redis: %v. Running without cache.", err)
			redisClient = nil
		} else {
			log.Println("Successfully connected to Redis.")
			defer redisClient.Close()
		}
	}

	// 4. Connect to NATS (Graceful / optional connection)
	var natsConn *nats.Conn
	if cfg.NatsAddress != "" {
		log.Printf("Connecting to NATS at %s...", cfg.NatsAddress)
		var err error
		natsConn, err = nats.Connect(cfg.NatsAddress)
		if err != nil {
			log.Printf("Warning: Failed to connect to NATS: %v. Running without event streaming.", err)
			natsConn = nil
		} else {
			log.Println("Successfully connected to NATS.")
			defer natsConn.Close()

			// Example: Background subscriber to demonstrate event usage
			_, err = natsConn.Subscribe("user.accessed", func(msg *nats.Msg) {
				log.Printf("[NATS Subscriber Demo] Received 'user.accessed' event: %s", string(msg.Data))
			})
			if err != nil {
				log.Printf("Warning: Failed to subscribe to NATS subject: %v", err)
			}
		}
	}

	// 5. Initialize Gin router with default middleware (logger and recovery)
	r := gin.Default()

	// 6. Wire dependencies using Clean Architecture & Dependency Injection
	userRepo := repository.NewUserRepository(redisClient, natsConn)
	userUsecase := usecase.NewUserUsecase(userRepo)

	// 7. Register HTTP route handlers
	handler.RegisterUserRoutes(r, userUsecase)

	// 8. Start the HTTP server
	log.Printf("Server running at %s (%s)", cfg.ServerAddress, cfg.AppEnv)
	srv := &http.Server{
		Addr:    cfg.ServerAddress,
		Handler: r,
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Failed to start server: %v", err)
	}
}
