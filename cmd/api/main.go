package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	_ "go.uber.org/automaxprocs"

	"backend-go/internal/config"
	"backend-go/internal/events"
	handlergrpc "backend-go/internal/handler/grpc"
	handlerhttp "backend-go/internal/handler/http"
	"backend-go/internal/pkg/health"
	"backend-go/internal/pkg/metrics"
	"backend-go/internal/pkg/middleware"
	"backend-go/internal/pkg/outbox"
	"backend-go/internal/pkg/shutdown"
	"backend-go/internal/pkg/telemetry"
	"backend-go/internal/repository"
	"backend-go/internal/usecase"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

func main() {
	// 1. Load application configuration
	cfg := config.LoadConfig()

	// 2. Initialize Structured Logger with dynamic LogLevel & Trace context handler
	var logLevel slog.Level
	switch cfg.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	baseHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})
	traceHandler := telemetry.NewTraceHandler(baseHandler)
	slog.SetDefault(slog.New(traceHandler))

	// 3. Start isolated internal pprof diagnostic server
	pprofSrv := StartPprofServer(cfg.PprofAddress)

	// 4. Set Gin Mode
	if cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	// 5. Initialize Prometheus Metrics & Health Manager
	promMetrics := metrics.InitMetrics(nil)
	healthMgr := health.NewManager(2 * time.Second)

	// 6. Initialize Redis Client (Graceful fallback)
	var redisClient *redis.Client
	if cfg.RedisAddress != "" {
		slog.Info("Connecting to Redis", "address", cfg.RedisAddress)
		redisClient = redis.NewClient(&redis.Options{
			Addr: cfg.RedisAddress,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := redisClient.Ping(ctx).Err(); err != nil {
			slog.Warn("Failed to connect to Redis, running without cache", "error", err)
			redisClient = nil
		} else {
			slog.Info("Successfully connected to Redis")
			healthMgr.Register("redis", health.CheckerFunc(func(c context.Context) error {
				return redisClient.Ping(c).Err()
			}))
		}
		cancel()
	}

	// 7. Initialize NATS Connection (Graceful fallback)
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
			healthMgr.Register("nats", health.CheckerFunc(func(c context.Context) error {
				if !natsConn.IsConnected() {
					return errors.New("nats disconnected")
				}
				return nil
			}))
		}
	}

	// 8. Wire Domain Dependencies
	eventPublisher := events.NewNATSEventPublisher(natsConn)
	userRepo := repository.NewUserRepository(redisClient, cfg.CacheTTL)
	userUsecase := usecase.NewUserUsecase(userRepo, eventPublisher)

	// 9. Initialize Outbox Worker & Retention Cleaner
	outboxRepo := outbox.NewMemoryRepository()
	var outboxLock outbox.DistributedLock
	if redisClient != nil {
		outboxLock = outbox.NewRedisDistributedLock(redisClient)
	}

	outboxWorker := outbox.NewWorker(outbox.DefaultConfig(), outboxRepo, outboxLock, outbox.MessagePublisher(nil))
	outboxCleaner := outbox.NewCleaner(outbox.DefaultCleanerConfig(), outboxRepo, outboxLock)

	outboxCtx, outboxCancel := context.WithCancel(context.Background())
	if outboxLock != nil {
		outboxWorker.Start(outboxCtx)
		outboxCleaner.Start(outboxCtx)
	}

	// 10. Setup HTTP Router & Hardened Middleware Pipeline
	r := gin.New()
	r.Use(middleware.RecoveryMiddleware())
	r.Use(middleware.SecurityHeaders())
	r.Use(middleware.CORS(middleware.DefaultCORSConfig()))
	r.Use(telemetry.HTTPMiddleware())
	r.Use(promMetrics.HTTPMiddleware())
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger())
	r.Use(middleware.TimeoutMiddleware(10 * time.Second))

	// Register System & Observability Endpoints
	r.GET("/healthz/live", healthMgr.LivenessHandler)
	r.GET("/healthz/ready", healthMgr.ReadinessHandler)
	r.GET("/metrics", metrics.Handler())

	// Register Business Routes
	handlerhttp.RegisterUserRoutes(r, userUsecase)

	// 11. Setup gRPC Server with Panic Recovery and Tracing
	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			middleware.GRPCUnaryRecoveryInterceptor(),
			telemetry.UnaryServerTraceInterceptor(),
			promMetrics.GRPCUnaryInterceptor(),
			middleware.GRPCUnaryTimeoutInterceptor(10*time.Second),
		),
	)
	_ = handlergrpc.NewUserGRPCHandler(userUsecase)

	// 12. Configure & Start HTTP Server
	httpSrv := &http.Server{
		Addr:         cfg.ServerAddress,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	go func() {
		slog.Info("HTTP Server is starting", "address", cfg.ServerAddress, "env", cfg.AppEnv)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Failed to start HTTP server", "error", err)
		}
	}()

	// 13. Configure & Start gRPC Server
	grpcLis, err := net.Listen("tcp", ":50051")
	if err == nil {
		go func() {
			slog.Info("gRPC Server is starting", "address", ":50051")
			if err := grpcSrv.Serve(grpcLis); err != nil {
				slog.Warn("gRPC server terminated", "error", err)
			}
		}()
	}

	// 14. Phased Graceful Shutdown Engine
	shutdownEngine := shutdown.NewEngine(2*time.Second, cfg.ShutdownTimeout)

	// Phase 1: Ingress / Endpoint drain (flip readiness probe)
	shutdownEngine.AddPhase(shutdown.Task{
		Name: "Deregister Readiness Probe",
		Fn: func(ctx context.Context) error {
			healthMgr.SetReady(false)
			return nil
		},
	})

	// Phase 2: Stop HTTP, gRPC, and pprof Listeners
	shutdownEngine.AddPhase(
		shutdown.Task{
			Name: "Shutdown HTTP Server",
			Fn: func(ctx context.Context) error {
				return httpSrv.Shutdown(ctx)
			},
		},
		shutdown.Task{
			Name: "Graceful Stop gRPC Server",
			Fn: func(ctx context.Context) error {
				grpcSrv.GracefulStop()
				return nil
			},
		},
		shutdown.Task{
			Name: "Shutdown pprof Server",
			Fn: func(ctx context.Context) error {
				return pprofSrv.Shutdown(ctx)
			},
		},
	)

	// Phase 3: Stop background Outbox Worker & Cleaner
	shutdownEngine.AddPhase(shutdown.Task{
		Name: "Stop Outbox Worker and Cleaner",
		Fn: func(ctx context.Context) error {
			outboxCancel()
			outboxWorker.Stop()
			outboxCleaner.Stop()
			return nil
		},
	})

	// Phase 4: Close Connection Pools & Clients
	shutdownEngine.AddPhase(
		shutdown.Task{
			Name: "Close Redis Client",
			Fn: func(ctx context.Context) error {
				if redisClient != nil {
					return redisClient.Close()
				}
				return nil
			},
		},
		shutdown.Task{
			Name: "Close NATS Connection",
			Fn: func(ctx context.Context) error {
				if natsConn != nil {
					natsConn.Close()
				}
				return nil
			},
		},
	)

	// Block until signal received and execute phased shutdown
	if err := shutdownEngine.ListenAndServe(); err != nil {
		slog.Error("Shutdown completed with error", "error", err)
	}
}
