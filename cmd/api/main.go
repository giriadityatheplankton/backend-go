package main

import (
	"log"
	"net/http"

	"backend-go/internal/config"
	"backend-go/internal/handler"
	"backend-go/internal/repository"
	"backend-go/internal/usecase"

	"github.com/gin-gonic/gin"
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

	// 3. Initialize Gin router with default middleware (logger and recovery)
	r := gin.Default()

	// 4. Wire dependencies using Clean Architecture & Dependency Injection
	userRepo := repository.NewUserRepository()
	userUsecase := usecase.NewUserUsecase(userRepo)

	// 5. Register HTTP route handlers
	handler.RegisterUserRoutes(r, userUsecase)

	// 6. Start the HTTP server
	log.Printf("Server running at %s (%s)", cfg.ServerAddress, cfg.AppEnv)
	srv := &http.Server{
		Addr:    cfg.ServerAddress,
		Handler: r,
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Failed to start server: %v", err)
	}
}
