package resilience

import (
	"context"
	"net/http"
	"time"

	"github.com/sony/gobreaker/v2"
)

// CircuitBreakerConfig holds settings for gobreaker integration.
type CircuitBreakerConfig struct {
	Name                string
	MaxRequests         uint32
	Interval            time.Duration
	Timeout             time.Duration
	ConsecutiveFailures uint32
	OnStateChange       func(name string, from gobreaker.State, to gobreaker.State)
}

// CircuitBreaker wraps sony/gobreaker with execution and fallback logic.
type CircuitBreaker struct {
	gb *gobreaker.CircuitBreaker[interface{}]
}

// NewCircuitBreaker initializes a sony/gobreaker CircuitBreaker instance.
func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	if cfg.ConsecutiveFailures == 0 {
		cfg.ConsecutiveFailures = 5
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}

	st := gobreaker.Settings{
		Name:          cfg.Name,
		MaxRequests:   cfg.MaxRequests,
		Interval:      cfg.Interval,
		Timeout:       cfg.Timeout,
		OnStateChange: cfg.OnStateChange,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= cfg.ConsecutiveFailures
		},
	}

	return &CircuitBreaker{
		gb: gobreaker.NewCircuitBreaker[interface{}](st),
	}
}

// Execute wraps an operation with sony/gobreaker protection and optional fallback.
func (cb *CircuitBreaker) Execute(
	ctx context.Context,
	reqFunc func(ctx context.Context) (interface{}, error),
	fallbackFunc func(ctx context.Context, err error) (interface{}, error),
) (interface{}, error) {
	res, err := cb.gb.Execute(func() (interface{}, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		return reqFunc(ctx)
	})

	if err != nil && fallbackFunc != nil {
		return fallbackFunc(ctx, err)
	}

	return res, err
}

// CurrentState returns the current state from sony/gobreaker.
func (cb *CircuitBreaker) CurrentState() gobreaker.State {
	return cb.gb.State()
}

// HTTPTransport wraps http.RoundTripper with sony/gobreaker.
type HTTPTransport struct {
	Breaker  *CircuitBreaker
	Fallback func(req *http.Request, err error) (*http.Response, error)
	Base     http.RoundTripper
}

func (t *HTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	res, err := t.Breaker.Execute(
		req.Context(),
		func(ctx context.Context) (interface{}, error) {
			return base.RoundTrip(req.WithContext(ctx))
		},
		func(ctx context.Context, originalErr error) (interface{}, error) {
			if t.Fallback != nil {
				return t.Fallback(req, originalErr)
			}
			return nil, originalErr
		},
	)

	if err != nil {
		return nil, err
	}
	return res.(*http.Response), nil
}
