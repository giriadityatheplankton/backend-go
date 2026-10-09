package migration_test

import (
	"context"
	"testing"

	"backend-go/internal/pkg/database/migration"

	"github.com/stretchr/testify/assert"
)

func TestMigrationEngine(t *testing.T) {
	engine := migration.NewEngine()
	assert.NotNil(t, engine)

	// Passing nil db returns expected error
	err := engine.Run(context.Background(), nil)
	assert.Error(t, err)
}
