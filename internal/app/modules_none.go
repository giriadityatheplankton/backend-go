//go:build !examples

package app

// buildModules returns the bounded contexts compiled into this binary. The bare
// skeleton ships none; add yours here. Build with -tags examples to include the
// order and user examples.
func buildModules(*moduleDeps) []Module { return nil }
