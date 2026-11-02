//go:build !examples

package migrations

import "backend-go/internal/platform/database/migrate"

func moduleSources() []migrate.Source { return nil }
