package parser

import (
	"strings"
	"testing"
)

func TestExtractGoImportsAndRefs(t *testing.T) {
	src := []byte(`package server

import (
	"context"
	"net/http"

	"github.com/go-kit/kit/endpoint"
)

func Foo(ctx context.Context, ep endpoint.Endpoint) error {
	ep2 := endpoint.Nop
	http.Handle("/x", ep2)
	return nil
}
`)
	fd, err := Parse(Go, "server.go", src)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("imports: %v", fd.Imports)
	t.Logf("refs: %v", fd.Refs)
	if len(fd.Imports) == 0 {
		t.Fatal("expected imports")
	}
	foundEp := false
	for _, imp := range fd.Imports {
		if strings.Contains(imp.Target, "go-kit/kit/endpoint") {
			foundEp = true
		}
	}
	if !foundEp {
		t.Errorf("expected endpoint import, got %v", fd.Imports)
	}
	if len(fd.Refs) == 0 {
		t.Fatalf("expected refs for endpoint.Nop and http.Handle")
	}
}

func TestSplitImport(t *testing.T) {
	cases := []struct{ lang Language; target, wantAlias, wantPkg string }{
		{Go, "github.com/go-kit/kit/endpoint", "endpoint", "github.com/go-kit/kit/endpoint"},
		{Go, "net/http", "http", "net/http"},
		{Python, "import services.token as t", "t", "services.token"},
		{Python, "from services.auth import login", "services.auth", "services.auth"},
		{Python, "import os", "os", "os"},
		{JavaScript, "./helper", "helper", "./helper"},
	}
	for _, c := range cases {
		gotA, gotP := splitImport(c.lang, c.target)
		if gotA != c.wantAlias || gotP != c.wantPkg {
			t.Errorf("splitImport(%s %q) = (%q, %q), want (%q, %q)", c.lang, c.target, gotA, gotP, c.wantAlias, c.wantPkg)
		}
	}
}

func TestExtractPythonRefs(t *testing.T) {
	src := []byte(`import services.token as t
from services.auth import login

def main():
    tok = t.issue(42)
    login("alice")
    return tok
`)
	fd, err := Parse(Python, "main.py", src)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("imports: %v", fd.Imports)
	t.Logf("refs: %v", fd.Refs)
	if len(fd.Imports) != 2 {
		t.Errorf("imports = %d, want 2", len(fd.Imports))
	}
	found := false
	for _, r := range fd.Refs {
		if r.Sym == "issue" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected t.issue ref, got %v", fd.Refs)
	}
}

func TestExtractJSRefs(t *testing.T) {
	src := []byte(`import { helper } from './helper';
const r = require('retry');
helper.go();
r.Retry();
`)
	fd, err := Parse(JavaScript, "main.js", src)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("imports: %v", fd.Imports)
	t.Logf("refs: %v", fd.Refs)
}

func TestExtractBareImportRefs(t *testing.T) {
	src := []byte(`import transformMediaTypeObject from "./media-type-object.js";
import { helper, other as renamed } from "./utils.js";

function use() {
  transformMediaTypeObject();
  helper();
  renamed();
}
`)
	fd, err := Parse(TypeScript, "x.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("imports: %v", fd.Imports)
	t.Logf("refs: %v", fd.Refs)
	// The bare identifiers used as calls should be captured as refs.
	found := map[string]bool{}
	for _, r := range fd.Refs {
		found[r.Sym] = true
	}
	for _, want := range []string{"transformMediaTypeObject", "helper", "renamed"} {
		if !found[want] {
			t.Errorf("expected ref for %s, got %v", want, fd.Refs)
		}
	}
}
