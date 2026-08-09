package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config holds all configuration values for the application.
type Config struct {
	ServerAddress string // HTTP server bind address (e.g., "127.0.0.1:8080")
	AppEnv        string // Application environment (e.g., "development", "production")
}

// LoadConfig loads the configuration from environment variables or a .env file.
func LoadConfig() *Config {
	// Attempt to load .env file optionally (useful for local development).
	if err := godotenv.Load(); err != nil {
		log.Println("Info: .env file not found, using system environment variables.")
	}

	serverAddr := os.Getenv("SERVER_ADDRESS")
	if serverAddr == "" {
		serverAddr = "127.0.0.1:8080" // Secure default for local development (localhost only).
	}

	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "development"
	}

	return &Config{
		ServerAddress: serverAddr,
		AppEnv:        appEnv,
	}
}
