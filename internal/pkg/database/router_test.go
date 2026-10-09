package database_test

import (
	"context"
	"testing"

	"backend-go/internal/pkg/database"

	"github.com/stretchr/testify/assert"
)

func TestDynamicTenantRouter(t *testing.T) {
	defaultDB := database.NewDBGroup(nil)
	tenantADB := database.NewDBGroup(nil)

	router := database.NewDynamicTenantRouter(defaultDB)
	router.Register("tenant-alpha", tenantADB)

	// Context with Tenant Alpha
	ctxTenantA := database.WithTenantID(context.Background(), "tenant-alpha")
	groupA, err := router.Resolve(ctxTenantA)
	assert.NoError(t, err)
	assert.Equal(t, tenantADB, groupA)

	// Context without tenant falls back to defaultDB
	groupDefault, err := router.Resolve(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, defaultDB, groupDefault)
}
