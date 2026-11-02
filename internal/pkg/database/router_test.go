package database_test

import (
	"context"
	"database/sql"
	"testing"

	"backend-go/internal/pkg/database"

	"github.com/stretchr/testify/assert"
)

func TestDBGroupReadPrimary(t *testing.T) {
	primary := &sql.DB{}
	replica := &sql.DB{}
	group := database.NewDBGroup(primary, replica)

	assert.Same(t, primary, group.ReadPrimary())
	assert.Same(t, primary, group.Write())
	assert.Same(t, replica, group.Read())
}

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

	// No tenant, or an unknown tenant, must NOT silently reach the shared database.
	_, err = router.Resolve(context.Background())
	assert.ErrorIs(t, err, database.ErrTenantNotFound)
	_, err = router.Resolve(database.WithTenantID(context.Background(), "tenant-unknown"))
	assert.ErrorIs(t, err, database.ErrTenantNotFound)

	// Explicit opt-in restores the fallback.
	router.AllowDefaultFallback()
	groupDefault, err := router.Resolve(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, defaultDB, groupDefault)
}

func TestReadSkipsUnhealthyReplicasAndFallsBackToPrimary(t *testing.T) {
	primary, r0, r1 := &sql.DB{}, &sql.DB{}, &sql.DB{}
	g := database.NewDBGroup(primary, r0, r1)

	g.MarkReplica(0, false)
	for i := 0; i < 6; i++ {
		assert.Same(t, r1, g.Read(), "only the healthy replica may serve reads")
	}
	g.MarkReplica(1, false)
	assert.Same(t, primary, g.Read(), "all replicas down: use the primary")
	assert.False(t, g.ReplicaHealthy(0))
	g.MarkReplica(0, true)
	assert.Same(t, r0, g.Read())
}
