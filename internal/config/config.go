// Package config loads and validates configuration from the environment.
//
// Invalid values are an error, never a silent fallback: a typo such as
// DB_MAX_OPEN_CONNS=abc must stop the process at startup, not run with a
// default nobody chose. Only app/ and cmd/ read this package; everything else
// receives plain values.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Redis describes one Redis deployment (state or cache).
type Redis struct {
	Mode             string // single | sentinel | cluster
	Addresses        []string
	Username         string
	Password         string `json:"-"`
	DB               int
	MasterName       string
	SentinelPassword string `json:"-"`
	TLS              bool
	TLSSkipVerify    bool
	DialTimeout      time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	PoolSize         int
	MinIdleConns     int
}

// Config holds all configuration values for the application.
type Config struct {
	// Service identity (labels, key prefixes, consumer names).
	ServiceName    string
	ServiceVersion string
	Commit         string
	AppEnv         string
	LogLevel       string

	// HTTP
	HTTPAddress          string
	AdminAddress         string
	PprofEnabled         bool
	ReadHeaderTimeout    time.Duration
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	IdleTimeout          time.Duration
	HTTPRequestTimeout   time.Duration
	MaxBodyBytes         int64
	TrustedProxies       []string
	CORSAllowedOrigins   []string
	CORSAllowCredentials bool

	// gRPC
	GRPCAddress        string
	GRPCRequestTimeout time.Duration
	GRPCReflection     bool
	GRPCTLSCertFile    string
	GRPCTLSKeyFile     string

	// Shutdown (DrainDelay + ShutdownTimeout + PreStopSeconds must fit TerminationGraceSeconds)
	DrainDelay       time.Duration
	ShutdownTimeout  time.Duration
	PreStop          time.Duration
	TerminationGrace time.Duration

	// Auth
	AuthMode         string // none | static | jwt
	JWTHMACSecret    string `json:"-"`
	JWTIssuer        string
	JWTAudience      string
	AuthStaticTokens string `json:"-"` // token;subject;tenant;scope1,scope2|token2;...

	// PostgreSQL
	DBPrimaryDSN        string   `json:"-"`
	DBReplicaDSNs       []string `json:"-"`
	DBMaxOpenConns      int
	DBMaxIdleConns      int
	DBConnMaxLifetime   time.Duration
	DBConnMaxIdleTime   time.Duration
	DBStatementTimeout  time.Duration
	DBIdleInTxTimeout   time.Duration
	DBReplicaProbeEvery time.Duration

	// Redis: "state" holds idempotency keys, locks and streams (noeviction + AOF);
	// "cache" may evict. If the cache section is unset it reuses state's address,
	// which is fine for development but not recommended in production.
	RedisState Redis
	RedisCache Redis
	CacheTTL   time.Duration

	// Messaging (Redis Streams)
	StreamPrefix          string
	StreamMaxLen          int64
	ConsumerGroup         string
	ConsumerWorkers       int
	ConsumerBatchSize     int
	ConsumerMaxDeliveries int
	ConsumerClaimIdle     time.Duration
	InboxRetention        time.Duration

	// Outbox
	OutboxPartitions    int
	OutboxBatchSize     int
	OutboxPollInterval  time.Duration
	OutboxMaxRetries    int
	OutboxLease         time.Duration
	OutboxPreserveOrder bool
	OutboxRetention     time.Duration

	// Idempotency & rate limiting
	IdempotencyTTL         time.Duration
	IdempotencyInFlightTTL time.Duration
	IdempotencyFailureMode string // closed | open
	RateLimitRequests      int64
	RateLimitWindow        time.Duration
	RateLimitFailOpen      bool

	// Circuit breaker for outbound dependencies
	BreakerFailures int
	BreakerTimeout  time.Duration

	// Reconciliation
	ReconcileInterval  time.Duration
	ReconcileThreshold time.Duration

	// Process roles: when true, cmd/api also runs the outbox relay, consumers and jobs.
	// Prefer a separate cmd/worker deployment in production.
	EmbedWorker   bool
	RedisRequired bool // false lets a dev environment start without Redis (degraded)
}

// IsProduction reports whether the process runs in production.
func (c *Config) IsProduction() bool { return strings.EqualFold(c.AppEnv, "production") }

type loader struct {
	errs []error
}

func (l *loader) fail(key, msg string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(msg, args...)))
}

// Load reads the environment, applies defaults for UNSET values only, parses
// strictly and validates. A .env file is read only outside production so a file
// baked into an image cannot override the orchestrator's environment.
func Load() (*Config, error) {
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "development"
	}
	if !strings.EqualFold(env, "production") {
		for _, f := range []string{".env." + env, ".env"} {
			if err := godotenv.Load(f); err == nil {
				break
			}
		}
	}
	return FromEnv()
}

// FromEnv parses the current process environment without touching .env files.
func FromEnv() (*Config, error) {
	l := &loader{}
	env := l.str("APP_ENV", "development")
	prod := strings.EqualFold(env, "production")

	defaultLog := "info"
	if strings.EqualFold(env, "development") {
		defaultLog = "debug"
	}

	c := &Config{
		ServiceName:    l.str("SERVICE_NAME", "backend-go"),
		ServiceVersion: l.str("SERVICE_VERSION", "dev"),
		Commit:         l.str("SERVICE_COMMIT", "unknown"),
		AppEnv:         env,
		LogLevel:       l.str("LOG_LEVEL", defaultLog),

		HTTPAddress:          l.str("SERVER_ADDRESS", "127.0.0.1:8080"),
		AdminAddress:         l.str("ADMIN_ADDRESS", "127.0.0.1:9090"),
		PprofEnabled:         l.boolean("PPROF_ENABLED", !prod),
		ReadHeaderTimeout:    l.dur("READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:          l.dur("READ_TIMEOUT", 10*time.Second),
		WriteTimeout:         l.dur("WRITE_TIMEOUT", 15*time.Second),
		IdleTimeout:          l.dur("IDLE_TIMEOUT", 60*time.Second),
		HTTPRequestTimeout:   l.dur("HTTP_REQUEST_TIMEOUT", 10*time.Second),
		MaxBodyBytes:         l.int64("MAX_BODY_BYTES", 1<<20),
		TrustedProxies:       l.csv("TRUSTED_PROXIES", ""),
		CORSAllowedOrigins:   l.csv("CORS_ALLOWED_ORIGINS", ""),
		CORSAllowCredentials: l.boolean("CORS_ALLOW_CREDENTIALS", false),

		GRPCAddress:        l.str("GRPC_ADDRESS", "127.0.0.1:50051"),
		GRPCRequestTimeout: l.dur("GRPC_REQUEST_TIMEOUT", 10*time.Second),
		GRPCReflection:     l.boolean("GRPC_REFLECTION", !prod),
		GRPCTLSCertFile:    l.str("GRPC_TLS_CERT_FILE", ""),
		GRPCTLSKeyFile:     l.str("GRPC_TLS_KEY_FILE", ""),

		DrainDelay:       l.dur("DRAIN_DELAY", 2*time.Second),
		ShutdownTimeout:  l.dur("SHUTDOWN_TIMEOUT", 15*time.Second),
		PreStop:          l.dur("PRESTOP_DELAY", 3*time.Second),
		TerminationGrace: l.dur("TERMINATION_GRACE", 30*time.Second),

		AuthMode:         l.str("AUTH_MODE", "none"),
		JWTHMACSecret:    l.str("JWT_HMAC_SECRET", ""),
		JWTIssuer:        l.str("JWT_ISSUER", ""),
		JWTAudience:      l.str("JWT_AUDIENCE", ""),
		AuthStaticTokens: l.str("AUTH_STATIC_TOKENS", ""),

		DBPrimaryDSN:        l.str("DB_PRIMARY_DSN", ""),
		DBReplicaDSNs:       l.csv("DB_REPLICA_DSNS", ""),
		DBMaxOpenConns:      l.integer("DB_MAX_OPEN_CONNS", 25),
		DBMaxIdleConns:      l.integer("DB_MAX_IDLE_CONNS", 10),
		DBConnMaxLifetime:   l.dur("DB_CONN_MAX_LIFETIME", 30*time.Minute),
		DBConnMaxIdleTime:   l.dur("DB_CONN_MAX_IDLE_TIME", 5*time.Minute),
		DBStatementTimeout:  l.dur("DB_STATEMENT_TIMEOUT", 5*time.Second),
		DBIdleInTxTimeout:   l.dur("DB_IDLE_IN_TX_TIMEOUT", 15*time.Second),
		DBReplicaProbeEvery: l.dur("DB_REPLICA_PROBE_INTERVAL", 5*time.Second),

		CacheTTL: l.dur("CACHE_TTL", 5*time.Minute),

		StreamPrefix:          l.str("STREAM_PREFIX", ""),
		StreamMaxLen:          l.int64("STREAM_MAXLEN", 100_000),
		ConsumerGroup:         l.str("CONSUMER_GROUP", ""),
		ConsumerWorkers:       l.integer("CONSUMER_WORKERS", 8),
		ConsumerBatchSize:     l.integer("CONSUMER_BATCH_SIZE", 32),
		ConsumerMaxDeliveries: l.integer("CONSUMER_MAX_DELIVERIES", 5),
		ConsumerClaimIdle:     l.dur("CONSUMER_CLAIM_IDLE", time.Minute),
		InboxRetention:        l.dur("INBOX_RETENTION", 14*24*time.Hour),

		OutboxPartitions:    l.integer("OUTBOX_PARTITIONS", 8),
		OutboxBatchSize:     l.integer("OUTBOX_BATCH_SIZE", 100),
		OutboxPollInterval:  l.dur("OUTBOX_POLL_INTERVAL", 200*time.Millisecond),
		OutboxMaxRetries:    l.integer("OUTBOX_MAX_RETRIES", 5),
		OutboxLease:         l.dur("OUTBOX_LEASE", 30*time.Second),
		OutboxPreserveOrder: l.boolean("OUTBOX_PRESERVE_ORDER", true),
		OutboxRetention:     l.dur("OUTBOX_RETENTION", 7*24*time.Hour),

		IdempotencyTTL:         l.dur("IDEMPOTENCY_TTL", 24*time.Hour),
		IdempotencyInFlightTTL: l.dur("IDEMPOTENCY_INFLIGHT_TTL", 30*time.Second),
		IdempotencyFailureMode: l.str("IDEMPOTENCY_FAILURE_MODE", "closed"),
		RateLimitRequests:      l.int64("RATE_LIMIT_REQUESTS", 100),
		RateLimitWindow:        l.dur("RATE_LIMIT_WINDOW", time.Minute),
		RateLimitFailOpen:      l.boolean("RATE_LIMIT_FAIL_OPEN", true),

		BreakerFailures: l.integer("BREAKER_CONSECUTIVE_FAILURES", 5),
		BreakerTimeout:  l.dur("BREAKER_OPEN_TIMEOUT", 10*time.Second),

		ReconcileInterval:  l.dur("RECONCILE_INTERVAL", 5*time.Minute),
		ReconcileThreshold: l.dur("RECONCILE_THRESHOLD", 15*time.Minute),

		EmbedWorker:   l.boolean("EMBED_WORKER", !prod),
		RedisRequired: l.boolean("REDIS_REQUIRED", true),
	}
	if c.ConsumerGroup == "" {
		c.ConsumerGroup = c.ServiceName
	}
	stateAddr := l.str("REDIS_ADDRESS", "127.0.0.1:6379") // REDIS_ADDRESS is the legacy single-address name
	c.RedisState = l.redis("REDIS", Redis{Mode: "single", Addresses: []string{stateAddr}})
	if hasAny("REDIS_CACHE_ADDRESSES") {
		c.RedisCache = l.redis("REDIS_CACHE", Redis{Mode: "single"})
	} else {
		c.RedisCache = c.RedisState
	}

	if len(l.errs) > 0 {
		return nil, fmt.Errorf("invalid configuration: %w", errors.Join(l.errs...))
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (l *loader) redis(prefix string, def Redis) Redis {
	addrDef := strings.Join(def.Addresses, ",")
	r := Redis{
		Mode:             strings.ToLower(l.str(prefix+"_MODE", def.Mode)),
		Addresses:        l.csv(prefix+"_ADDRESSES", addrDef),
		Username:         l.str(prefix+"_USERNAME", ""),
		Password:         l.str(prefix+"_PASSWORD", ""),
		DB:               l.integer(prefix+"_DB", 0),
		MasterName:       l.str(prefix+"_MASTER_NAME", ""),
		SentinelPassword: l.str(prefix+"_SENTINEL_PASSWORD", ""),
		TLS:              l.boolean(prefix+"_TLS", false),
		TLSSkipVerify:    l.boolean(prefix+"_TLS_SKIP_VERIFY", false),
		DialTimeout:      l.dur(prefix+"_DIAL_TIMEOUT", 2*time.Second),
		ReadTimeout:      l.dur(prefix+"_READ_TIMEOUT", time.Second),
		WriteTimeout:     l.dur(prefix+"_WRITE_TIMEOUT", time.Second),
		PoolSize:         l.integer(prefix+"_POOL_SIZE", 0),
		MinIdleConns:     l.integer(prefix+"_MIN_IDLE_CONNS", 0),
	}
	return r
}

// Validate checks cross-field and production rules. All problems are reported together.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	switch strings.ToLower(c.AuthMode) {
	case "none", "static", "jwt":
	default:
		add("AUTH_MODE: %q is not one of none, static, jwt", c.AuthMode)
	}
	if c.AuthMode == "jwt" && len(c.JWTHMACSecret) < 32 {
		add("JWT_HMAC_SECRET: must be at least 32 characters when AUTH_MODE=jwt")
	}
	if c.AuthMode == "static" && c.AuthStaticTokens == "" {
		add("AUTH_STATIC_TOKENS: required when AUTH_MODE=static")
	}
	switch c.IdempotencyFailureMode {
	case "closed", "open":
	default:
		add("IDEMPOTENCY_FAILURE_MODE: must be \"closed\" or \"open\", got %q", c.IdempotencyFailureMode)
	}
	for name, r := range map[string]Redis{"REDIS": c.RedisState, "REDIS_CACHE": c.RedisCache} {
		switch r.Mode {
		case "single", "sentinel", "cluster":
		default:
			add("%s_MODE: must be single, sentinel or cluster, got %q", name, r.Mode)
		}
		if len(r.Addresses) == 0 {
			add("%s_ADDRESSES: at least one address is required", name)
		}
		if r.Mode == "cluster" && r.DB != 0 {
			add("%s_DB: must be 0 in cluster mode", name)
		}
		if r.Mode == "sentinel" && r.MasterName == "" {
			add("%s_MASTER_NAME: required in sentinel mode", name)
		}
		if r.Mode == "single" && len(r.Addresses) > 1 {
			add("%s_ADDRESSES: single mode takes exactly one address (did you mean REDIS_MODE=cluster?)", name)
		}
	}
	if c.DBMaxOpenConns <= 0 || c.DBMaxIdleConns < 0 || c.DBMaxIdleConns > c.DBMaxOpenConns {
		add("DB_MAX_OPEN_CONNS / DB_MAX_IDLE_CONNS: need open > 0 and 0 <= idle <= open")
	}
	if len(c.DBReplicaDSNs) > 0 && c.DBPrimaryDSN == "" {
		add("DB_PRIMARY_DSN: required when DB_REPLICA_DSNS is set")
	}
	if c.OutboxPartitions <= 0 || c.OutboxBatchSize <= 0 || c.OutboxMaxRetries <= 0 {
		add("OUTBOX_*: partitions, batch size and max retries must be positive")
	}
	if c.RateLimitRequests <= 0 || c.RateLimitWindow <= 0 {
		add("RATE_LIMIT_*: requests and window must be positive")
	}
	if c.ConsumerWorkers <= 0 || c.ConsumerBatchSize <= 0 || c.ConsumerMaxDeliveries <= 0 {
		add("CONSUMER_*: workers, batch size and max deliveries must be positive")
	}
	if c.BreakerFailures <= 0 || c.BreakerTimeout <= 0 {
		add("BREAKER_*: consecutive failures and open timeout must be positive")
	}
	if c.GRPCAddress == "" || c.HTTPAddress == "" {
		add("SERVER_ADDRESS / GRPC_ADDRESS: must not be empty")
	}
	if (c.GRPCTLSCertFile == "") != (c.GRPCTLSKeyFile == "") {
		add("GRPC_TLS_CERT_FILE / GRPC_TLS_KEY_FILE: set both or neither")
	}

	// Timing relationships that, when wrong, cause subtle production bugs.
	if c.WriteTimeout < c.HTTPRequestTimeout {
		add("WRITE_TIMEOUT (%s) must be >= HTTP_REQUEST_TIMEOUT (%s) or responses are cut off", c.WriteTimeout, c.HTTPRequestTimeout)
	}
	margin := 5 * time.Second
	if need := maxDuration(c.HTTPRequestTimeout, c.GRPCRequestTimeout) + margin; c.IdempotencyInFlightTTL < need {
		add("IDEMPOTENCY_INFLIGHT_TTL (%s) must be >= request timeout + %s (%s), otherwise the lease can expire while the handler still runs",
			c.IdempotencyInFlightTTL, margin, need)
	}
	if c.IdempotencyTTL <= c.IdempotencyInFlightTTL {
		add("IDEMPOTENCY_TTL (%s) must exceed IDEMPOTENCY_INFLIGHT_TTL", c.IdempotencyTTL)
	}
	if total := c.DrainDelay + c.ShutdownTimeout + c.PreStop; total > c.TerminationGrace {
		add("DRAIN_DELAY + SHUTDOWN_TIMEOUT + PRESTOP_DELAY (%s) exceeds TERMINATION_GRACE (%s); Kubernetes would SIGKILL mid-shutdown", total, c.TerminationGrace)
	}
	if c.OutboxLease < 5*time.Second {
		add("OUTBOX_LEASE (%s) is too short; use at least 5s", c.OutboxLease)
	}

	if c.IsProduction() {
		if c.AuthMode == "none" {
			add("AUTH_MODE: \"none\" is not allowed in production")
		}
		if c.DBPrimaryDSN == "" {
			add("DB_PRIMARY_DSN: required in production")
		}
		if !c.RedisRequired {
			add("REDIS_REQUIRED: must be true in production (idempotency, locks and streams depend on it)")
		}
		if c.GRPCReflection {
			add("GRPC_REFLECTION: must be false in production")
		}
		for _, o := range c.CORSAllowedOrigins {
			if o == "*" && c.CORSAllowCredentials {
				add("CORS_ALLOWED_ORIGINS: \"*\" with CORS_ALLOW_CREDENTIALS=true lets any site act as the user")
			}
		}
		if c.RedisState.TLSSkipVerify || c.RedisCache.TLSSkipVerify {
			add("REDIS_TLS_SKIP_VERIFY: must be false in production")
		}
		if c.PprofEnabled && !isLoopbackOrInternal(c.AdminAddress) {
			add("PPROF_ENABLED: requires ADMIN_ADDRESS bound to a loopback/internal address")
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return nil
}

func isLoopbackOrInternal(addr string) bool {
	return strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "localhost") || strings.HasPrefix(addr, "[::1]")
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func hasAny(key string) bool { v, ok := os.LookupEnv(key); return ok && v != "" }

func (l *loader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func (l *loader) csv(key, def string) []string {
	v := def
	if x, ok := os.LookupEnv(key); ok {
		v = x
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (l *loader) integer(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.fail(key, "%q is not an integer", v)
		return def
	}
	return n
}

func (l *loader) int64(key string, def int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		l.fail(key, "%q is not an integer", v)
		return def
	}
	return n
}

func (l *loader) boolean(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(key, "%q is not a boolean", v)
		return def
	}
	return b
}

func (l *loader) dur(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.fail(key, "%q is not a duration such as 500ms, 10s or 5m (a bare number like 10 is invalid)", v)
		return def
	}
	return d
}
