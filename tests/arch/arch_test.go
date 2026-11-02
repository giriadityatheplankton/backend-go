// Package arch_test enforces the Dependency Rule of Clean Architecture on every
// build: source-code dependencies may only point inward. It parses imports, so it
// also checks files behind build tags (for example the examples modules).
//
//	kernel  <-  modules/<m>/domain  <-  modules/<m>/usecase  <-  modules/<m>/adapter  <-  modules/<m>  <-  app, cmd
//	platform never imports modules, app or config
package arch_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const mod = "backend-go/"

type layer int

const (
	other layer = iota
	kernel
	domain
	usecase
	inbound
	outbound
	moduleRoot
	platform
	appLayer
	config
	cmdLayer
)

var moduleRe = regexp.MustCompile(`^internal/modules/([^/]+)(?:/(.*))?$`)

// classify returns the layer and owning module of a directory (relative to the repo root).
func classify(dir string) (layer, string) {
	if m := moduleRe.FindStringSubmatch(dir); m != nil {
		name, rest := m[1], m[2]
		switch {
		case rest == "":
			return moduleRoot, name
		case rest == "domain" || strings.HasPrefix(rest, "domain/"):
			return domain, name
		case rest == "usecase" || strings.HasPrefix(rest, "usecase/"):
			return usecase, name
		case strings.HasPrefix(rest, "adapter/inbound"):
			return inbound, name
		case strings.HasPrefix(rest, "adapter/outbound"):
			return outbound, name
		}
		return other, name
	}
	switch {
	case strings.HasPrefix(dir, "internal/kernel"):
		return kernel, ""
	case strings.HasPrefix(dir, "internal/platform"):
		return platform, ""
	case dir == "internal/app":
		return appLayer, ""
	case dir == "internal/config":
		return config, ""
	case strings.HasPrefix(dir, "cmd/"):
		return cmdLayer, ""
	}
	return other, ""
}

// frameworks must never leak into the two innermost business layers.
var frameworkPrefixes = []string{
	"database/sql", "net/http", "github.com/gin-gonic", "google.golang.org/grpc", "google.golang.org/protobuf",
	"github.com/redis", "github.com/jackc", "github.com/prometheus", "github.com/golang-jwt", mod + "gen",
}

func hasPrefix(path string, prefixes ...string) bool {
	for _, p := range prefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

func isStdlib(path string) bool {
	return !strings.Contains(strings.SplitN(path, "/", 2)[0], ".") && !strings.HasPrefix(path, mod)
}

// violation returns a reason if importing imp from (from, fromMod) is forbidden.
func violation(from layer, fromMod, imp string) string {
	if !strings.HasPrefix(imp, mod) && !isStdlib(imp) {
		// third-party library
		if from == kernel || from == domain || from == usecase {
			return "third-party library in an inner layer"
		}
		return ""
	}
	if isStdlib(imp) {
		if (from == domain || from == usecase || from == kernel) && hasPrefix(imp, frameworkPrefixes...) {
			return "framework/IO package in an inner layer (" + imp + ")"
		}
		return ""
	}
	rel := strings.TrimPrefix(imp, mod)
	toLayer, toMod := classify(rel)

	if hasPrefix(imp, frameworkPrefixes...) && (from == domain || from == usecase) {
		return "framework package in an inner layer"
	}
	crossModule := toLayer >= domain && toLayer <= moduleRoot && toMod != "" && toMod != fromMod
	switch from {
	case kernel:
		if toLayer != kernel {
			return "kernel may import only the standard library and kernel"
		}
	case domain:
		if toLayer != kernel && (toLayer != domain || crossModule) {
			return "domain may import only kernel and its own domain"
		}
	case usecase:
		if crossModule {
			return "use cases must not import another module (communicate via events or ports)"
		}
		if toLayer != kernel && toLayer != domain && toLayer != usecase {
			return "use cases may import only kernel, domain and ports"
		}
	case inbound:
		if crossModule {
			return "adapters must not import another module"
		}
		if toLayer == outbound || toLayer == appLayer || toLayer == config || toLayer == moduleRoot {
			return "inbound adapters must not import outbound adapters, app or config"
		}
	case outbound:
		if crossModule {
			return "adapters must not import another module"
		}
		if toLayer == inbound || toLayer == appLayer || toLayer == config || toLayer == moduleRoot {
			return "outbound adapters must not import inbound adapters, app or config"
		}
	case moduleRoot:
		if crossModule || toLayer == appLayer || toLayer == config {
			return "a module may not import other modules, app or config"
		}
	case platform:
		if toLayer == moduleRoot || toLayer == domain || toLayer == usecase || toLayer == inbound || toLayer == outbound ||
			toLayer == appLayer || toLayer == config {
			return "platform is domain-agnostic: it must not import modules, app or config"
		}
	}
	return ""
}

func TestDependencyRule(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "gen", "node_modules", "tests":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		rel = filepath.ToSlash(rel)
		from, fromMod := classify(rel)
		if from == other {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		checked++
		isTest := strings.HasSuffix(path, "_test.go")
		for _, spec := range f.Imports {
			imp, _ := strconv.Unquote(spec.Path.Value)
			if isTest && !strings.HasPrefix(imp, mod) {
				continue // tests may use testify, miniredis, ...
			}
			if why := violation(from, fromMod, imp); why != "" {
				t.Errorf("%s imports %q: %s", filepath.ToSlash(path[len(root)+1:]), imp, why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 20 {
		t.Fatalf("architecture test scanned only %d files; is the repo layout what it expects?", checked)
	}
}

func TestClassifierSelfCheck(t *testing.T) {
	for _, c := range []struct {
		dir string
		l   layer
		m   string
	}{
		{"internal/modules/order/domain", domain, "order"},
		{"internal/modules/order/usecase/port", usecase, "order"},
		{"internal/modules/order/adapter/inbound/http", inbound, "order"},
		{"internal/modules/order/adapter/outbound/postgres", outbound, "order"},
		{"internal/modules/order", moduleRoot, "order"},
		{"internal/platform/outbox", platform, ""},
		{"internal/kernel/apperr", kernel, ""},
	} {
		l, m := classify(c.dir)
		if l != c.l || m != c.m {
			t.Errorf("classify(%q) = %v,%q want %v,%q", c.dir, l, m, c.l, c.m)
		}
	}
	// The rules themselves must reject what they claim to reject.
	bad := []struct {
		from layer
		mod  string
		imp  string
	}{
		{domain, "order", mod + "internal/modules/order/usecase"},
		{domain, "order", "github.com/gin-gonic/gin"},
		{usecase, "order", "database/sql"},
		{usecase, "order", mod + "internal/platform/database"},
		{usecase, "order", mod + "internal/modules/user/domain"},
		{inbound, "order", mod + "internal/modules/order/adapter/outbound/postgres"},
		{outbound, "order", mod + "internal/modules/order/adapter/inbound/http"},
		{platform, "", mod + "internal/modules/order"},
		{platform, "", mod + "internal/config"},
		{kernel, "", mod + "internal/platform/outbox"},
		{moduleRoot, "order", mod + "internal/modules/user"},
	}
	for _, b := range bad {
		if violation(b.from, b.mod, b.imp) == "" {
			t.Errorf("rule should forbid layer %v importing %s", b.from, b.imp)
		}
	}
	good := []struct {
		from layer
		mod  string
		imp  string
	}{
		{usecase, "order", mod + "internal/modules/order/domain"},
		{usecase, "order", mod + "internal/kernel/clock"},
		{inbound, "order", mod + "internal/platform/httpserver"},
		{inbound, "order", mod + "internal/modules/order/usecase"},
		{outbound, "order", mod + "internal/modules/order/usecase/port"},
		{moduleRoot, "order", mod + "internal/modules/order/adapter/inbound/http"},
		{domain, "order", "errors"},
	}
	for _, g := range good {
		if why := violation(g.from, g.mod, g.imp); why != "" {
			t.Errorf("rule should allow layer %v importing %s (%s)", g.from, g.imp, why)
		}
	}
	_ = os.Stdout
}
