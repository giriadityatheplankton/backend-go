package app

import (
	"time"

	"backend-go/internal/pkg/cache"
	"backend-go/internal/pkg/database"
)

// moduleDeps is what app/ offers to module constructors (infrastructure only).
type moduleDeps struct {
	DB               *database.DBGroup
	Tx               *database.TxManager
	Cache            *cache.Manager
	CacheTTL         time.Duration
	OutboxPartitions int
}
