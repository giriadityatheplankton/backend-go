package shutdown_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"backend-go/internal/platform/shutdown"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadinessFlipsBeforeDrainAndServersStopAfter(t *testing.T) {
	var mu sync.Mutex
	var events []string
	var tFlip, tStop time.Time
	rec := func(s string) { mu.Lock(); events = append(events, s); mu.Unlock() }

	e := shutdown.NewEngine(80*time.Millisecond, time.Second)
	e.AddPreDrain(shutdown.Task{Name: "readiness", Fn: func(context.Context) error { tFlip = time.Now(); rec("flip"); return nil }})
	e.AddPhase(shutdown.Task{Name: "http", Fn: func(context.Context) error { tStop = time.Now(); rec("http"); return nil }})
	e.AddPhase(shutdown.Task{Name: "pool", Fn: func(context.Context) error { rec("pool"); return nil }})

	require.NoError(t, e.Run())
	assert.Equal(t, []string{"flip", "http", "pool"}, events)
	assert.GreaterOrEqual(t, tStop.Sub(tFlip), 80*time.Millisecond, "servers must stop only after the drain delay")
}

func TestFailuresAreReturned(t *testing.T) {
	e := shutdown.NewEngine(0, time.Second)
	e.AddPhase(shutdown.Task{Name: "bad", Fn: func(context.Context) error { return errors.New("boom") }})
	err := e.Run()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad")
}

func TestPhaseTimeoutCancelsHungTask(t *testing.T) {
	e := shutdown.NewEngine(0, 50*time.Millisecond)
	e.AddPhase(shutdown.Task{Name: "hang", Fn: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }})
	start := time.Now()
	require.Error(t, e.Run())
	assert.Less(t, time.Since(start), time.Second)
}

func TestMinTerminationGrace(t *testing.T) {
	e := shutdown.NewEngine(2*time.Second, 15*time.Second)
	assert.Equal(t, 20*time.Second, e.MinTerminationGrace(3*time.Second))
}
