// Package migrations embeds the SQL schema, split per owner so that platform
// tables (outbox, inbox, audit) and each module's tables have independent
// version spaces. Removing a module never leaves a hole in anyone's numbering.
package migrations

import (
	"embed"

	"backend-go/internal/pkg/database/migrate"
)

//go:embed platform/*.sql
var platformFS embed.FS

// Sources returns the migration sources in apply order: platform first, then modules.
func Sources() []migrate.Source {
	return append([]migrate.Source{{Name: "platform", FS: platformFS, Dir: "platform"}}, moduleSources()...)
}
