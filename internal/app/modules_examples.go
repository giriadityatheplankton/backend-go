//go:build examples

package app

import (
	"log/slog"

	"backend-go/internal/modules/order"
	"backend-go/internal/modules/user"
)

func buildModules(d *moduleDeps) []Module {
	mods := []Module{user.New(user.Deps{Cache: d.Cache, CacheTTL: d.CacheTTL})}
	if d.DB != nil {
		mods = append(mods, order.New(order.Deps{
			DB: d.DB, Tx: d.Tx, Cache: d.Cache, CacheTTL: d.CacheTTL, OutboxPartitions: d.OutboxPartitions,
		}))
	} else {
		slog.Warn("order module disabled: PostgreSQL is not configured")
	}
	return mods
}
