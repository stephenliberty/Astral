package graph

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stephen/advisor/internal/index"
	"github.com/stephen/advisor/internal/store"
)

// buildFixture writes a small multi-package repo and indexes it.
func buildFixture(t *testing.T) (*Graph, *store.Index) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"endpoint/endpoint.go": `package endpoint

// Endpoint is the basic unit.
type Endpoint func(ctx interface{}, req interface{}) (interface{}, error)

// Nop is a no-op endpoint.
func Nop(ctx interface{}, req interface{}) (interface{}, error) { return nil, nil }
`,
		"endpoint/endpoint_test.go": `package endpoint

import "testing"

func TestNop(t *testing.T) {
	if _, err := Nop(nil, nil); err != nil {
		t.Fatal(err)
	}
}
`,
		"transport/http/server.go": `package http

import (
	"context"

	"github.com/acme/kit/endpoint"
)

func Handle(ctx context.Context, ep endpoint.Endpoint) error {
	_, err := endpoint.Nop(ctx, nil)
	return err
}
`,
		"transport/http/server_test.go": `package http

import (
	"context"
	"testing"

	"github.com/acme/kit/endpoint"
)

func TestHandle(t *testing.T) {
	ep := endpoint.Nop
	if err := Handle(context.Background(), ep); err != nil {
		t.Fatal(err)
	}
}
`,
		"auth/middleware.go": `package auth

import (
	"context"

	"github.com/acme/kit/endpoint"
)

// Wrap wraps an endpoint.
func Wrap(next endpoint.Endpoint) endpoint.Endpoint {
	return func(ctx context.Context, req interface{}) (interface{}, error) {
		return next(ctx, req)
	}
}
`,
		"auth/main.py": `from auth import wrap

def run():
    ep = wrap(echo)
    return ep
`,
	}
	for path, content := range files {
		fp := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix := index.New(root)
	idx, err := ix.Init()
	if err != nil {
		t.Fatal(err)
	}
	return New(root), idx
}

func TestIsTestFile(t *testing.T) {
	cases := map[string]bool{
		"foo_test.go":          true,
		"foo_spec.py":          true,
		"foo.test.ts":          true,
		"foo.go":               false,
		"endpoint_test.go":     true,
		"endpoint_example.go":  false,
	}
	for path, want := range cases {
		if got := IsTestFile(path); got != want {
			t.Errorf("IsTestFile(%s) = %v, want %v", path, got, want)
		}
	}
}

func TestCallersOfSymbol(t *testing.T) {
	g, idx := buildFixture(t)
	callers, err := g.CallersOfSymbol(idx, "Nop")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range callers {
		got[c.File] = true
	}
	if !got["transport/http/server.go"] {
		t.Errorf("expected server.go to call Nop, got %v", callers)
	}
	if !got["transport/http/server_test.go"] {
		t.Errorf("expected server_test.go to call Nop, got %v", callers)
	}
	if len(callers) != 2 {
		t.Errorf("expected 2 callers of Nop, got %d: %v", len(callers), callers)
	}
}

func TestAffectedTests(t *testing.T) {
	g, idx := buildFixture(t)
	tests, err := g.AffectedTests(idx, []string{"endpoint/endpoint.go"})
	if err != nil {
		t.Fatal(err)
	}
	// endpoint_test.go (same package), transport/http/server_test.go and
	// auth tests transitively (auth wraps endpoint, but auth has no _test.go).
	got := map[string]bool{}
	for _, tt := range tests {
		got[tt] = true
	}
	if !got["endpoint/endpoint_test.go"] {
		t.Errorf("expected endpoint_test.go affected, got %v", tests)
	}
	if !got["transport/http/server_test.go"] {
		t.Errorf("expected transport/http/server_test.go affected, got %v", tests)
	}
	// Only two test files exist; check nothing bogus.
	for _, tt := range tests {
		if tt == "auth/main.py" {
			t.Errorf("non-test file should not be reported")
		}
	}
}
