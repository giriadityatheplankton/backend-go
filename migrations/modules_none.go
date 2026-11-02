//go:build !examples

package migrations

import "backend-go/internal/pkg/database/migrate"

func moduleSources() []migrate.Source { return nil }
