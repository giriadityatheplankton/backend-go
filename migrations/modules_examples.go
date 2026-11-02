//go:build examples

package migrations

import (
	"embed"

	"backend-go/internal/platform/database/migrate"
)

//go:embed order/*.sql
var orderFS embed.FS

//go:embed user/*.sql
var userFS embed.FS

func moduleSources() []migrate.Source {
	return []migrate.Source{
		{Name: "order", FS: orderFS, Dir: "order"},
		{Name: "user", FS: userFS, Dir: "user"},
	}
}
