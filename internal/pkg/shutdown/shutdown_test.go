package shutdown_test

import (
	"context"
	"testing"
	"time"

	"backend-go/internal/pkg/shutdown"

	"github.com/stretchr/testify/assert"
)

func TestShutdownEngineStructure(t *testing.T) {
	engine := shutdown.NewEngine(10*time.Millisecond, 50*time.Millisecond)
	assert.NotNil(t, engine)

	var executed bool
	engine.AddPhase(shutdown.Task{
		Name: "Test Cleanup",
		Fn: func(ctx context.Context) error {
			executed = true
			return nil
		},
	})

	assert.False(t, executed)
}
