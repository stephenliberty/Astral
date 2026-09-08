package parser

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tshcl "github.com/tree-sitter-grammars/tree-sitter-hcl/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tspy "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// Symbol is a definition extracted from a source file.
type Symbol struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Line    int    `json:"line"` // 1-based
	Col     int    `json:"col"`  // 0-based
	EndLine int    `json:"end_line"`
}

// FileData is the derived artifact stored for a parsed source file.
type FileData struct {
	Path     string     `json:"path"`
	Hash     string     `json:"hash"`
	Symbols  []Symbol   `json:"symbols"`
	Imports  []Import  `json:"imports,omitempty"`
	Refs     []Ref      `json:"refs,omitempty"`
	Summary  string     `json:"summary"`
}

// Import is a module this file imports from. Target is the module path as
// written in source (e.g. "github.com/go-kit/kit/endpoint" or "./helper").
type Import struct {
	Target string `json:"target"`
}

// Ref is a cross-package reference found in the file, like pkg.Sym or an
// identifier imported under a local alias.
type Ref struct {
	Pkg string `json:"pkg,omitempty"` // package/module name, empty for local
	Sym string `json:"sym"`           // referenced symbol name
}

// Language identifies a supported source language.
type Language string

const (
	Python     Language = "python"
	TypeScript Language = "typescript"
	JavaScript Language = "javascript"
	Go         Language = "go"
	HCL        Language = "hcl"
)

// DetectLanguage infers the language from a file path.
func DetectLanguage(path string) (Language, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py":
		return Python, true
	case ".ts", ".tsx", ".mts", ".cts":
		return TypeScript, true
	case ".js", ".jsx", ".mjs", ".cjs":
		return JavaScript, true
	case ".go":
		return Go, true
	case ".tf", ".hcl", ".tfvars":
		return HCL, true
	default:
		return "", false
	}
}

// Parse parses source content and extracts symbols and references.
func Parse(lang Language, path string, content []byte) (*FileData, error) {
	grammar, ok := grammarFor(lang)
	if !ok {
		return nil, fmt.Errorf("unsupported language: %s", lang)
	}
	p := tree_sitter.NewParser()
	defer p.Close()
	if err := p.SetLanguage(grammar); err != nil {
		return nil, fmt.Errorf("set language %s: %w", lang, err)
	}
	tree := p.Parse(content, nil)
	defer tree.Close()
	root := tree.RootNode()

	var symbols []Symbol
	walk(root, content, func(n *tree_sitter.Node) {
		if sym, ok := definitionFor(lang, n, content); ok {
			symbols = append(symbols, sym)
		}
	})
	imports := extractImports(lang, root, content)
	refs := extractRefs(lang, root, imports, content)

	return &FileData{
		Path:    path,
		Symbols: symbols,
		Imports: imports,
		Refs:    refs,
		Summary: summarize(lang, symbols),
	}, nil
}

// extractImports collects the modules a file imports.
func extractImports(lang Language, root *tree_sitter.Node, content []byte) []Import {
	var out []Import
	seen := map[string]bool{}
	walk(root, content, func(n *tree_sitter.Node) {
		var target string
		switch lang {
		case Go:
			if n.Kind() == "import_spec" {
				// Match both `import "x"` (parent=import_declaration) and the
				// block form (parent=import_spec_list).
				if n.NamedChildCount() > 0 {
					target = strings.TrimSpace(n.NamedChild(0).Utf8Text(content))
					target = strings.Trim(target, `"`)
				}
			}
		case Python:
			if n.Kind() == "import_statement" {
				// import os / import services.token
				target = n.Utf8Text(content)
			} else if n.Kind() == "import_from_statement" {
				target = n.Utf8Text(content)
			}
		case TypeScript, JavaScript:
			if n.Kind() == "import_statement" {
				// Use the source field: import { x } from './helper'
				if src := n.ChildByFieldName("source"); src != nil {
					target = strings.Trim(src.Utf8Text(content), "'\"")
				} else {
					target = n.Utf8Text(content)
				}
			} else if n.Kind() == "call_expression" {
				// require("z")
				if fn := n.ChildByFieldName("function"); fn != nil && fn.Utf8Text(content) == "require" {
					args := n.ChildByFieldName("arguments")
					if args != nil && args.NamedChildCount() > 0 {
						target = strings.Trim(args.NamedChild(0).Utf8Text(content), "'\"")
					}
				}
			}
		}
		if target != "" && !seen[target] {
			seen[target] = true
			out = append(out, Import{Target: strings.TrimSpace(target)})
		}
	})
	return out
}

// extractRefs finds cross-package references: pkg.Sym selector expressions
// where pkg is a configured package alias, and dotted names in Python.
func extractRefs(lang Language, root *tree_sitter.Node, imports []Import, content []byte) []Ref {
	// Track local aliases from imports: alias -> pkg.
	aliases := map[string]string{}
	for _, imp := range imports {
		alias, pkg := splitImport(lang, imp.Target)
		aliases[alias] = pkg
	}

	var out []Ref
	seen := map[Ref]bool{}
	walk(root, content, func(n *tree_sitter.Node) {
		switch lang {
		case Go, TypeScript, JavaScript:
			if n.Kind() == "selector_expression" || n.Kind() == "member_expression" {
				// Go: operand + field. JS/TS: object + property.
				left := n.ChildByFieldName("operand")
				if left == nil {
					left = n.ChildByFieldName("object")
				}
				right := n.ChildByFieldName("field")
				if right == nil {
					right = n.ChildByFieldName("property")
				}
				if left != nil && right != nil {
					base := left.Utf8Text(content)
					if pkg, ok := aliases[base]; ok {
						ref := Ref{Pkg: pkg, Sym: right.Utf8Text(content)}
						if ref.Sym != "" && !seen[ref] {
							seen[ref] = true
							out = append(out, ref)
						}
					}
				}
			}
		case Python:
			if n.Kind() == "call" {
				fn := n.ChildByFieldName("function")
				if fn == nil || fn.Kind() != "attribute" {
					return
				}
				// attribute = object.value, e.g. t.issue
				if fn.NamedChildCount() < 2 {
					return
				}
				obj := fn.NamedChild(0).Utf8Text(content)
				attr := fn.NamedChild(fn.NamedChildCount()-1).Utf8Text(content)
				if pkg, ok := aliases[obj]; ok {
					ref := Ref{Pkg: pkg, Sym: attr}
					if !seen[ref] {
						seen[ref] = true
						out = append(out, ref)
					}
				}
			}
		}
	})
	return out
}

// splitImport returns the local alias and the module path. For Go, the alias
// is the last path component and the module the full import path. For Python
// "from services.auth import login", the alias is the module prefix. For JS
// relative imports, the alias is the base filename.
func splitImport(lang Language, target string) (alias, pkg string) {
	target = strings.TrimSpace(target)
	switch lang {
	case Python:
		// "from services.auth import login" -> pkg=services.auth, sym=login
		if strings.HasPrefix(target, "from ") && strings.Contains(target, " import ") {
			mod := strings.TrimPrefix(target, "from ")
			mod = strings.SplitN(mod, " import ", 2)[0]
			return mod, mod
		}
		// "import services.token as t" -> t -> services.token
		if strings.HasPrefix(target, "import ") {
			rest := strings.TrimPrefix(target, "import ")
			if ss := strings.SplitN(rest, " as ", 2); len(ss) == 2 {
				return strings.TrimSpace(ss[1]), strings.TrimSpace(ss[0])
			}
			mod := rest
			base := mod
			if i := strings.LastIndex(mod, "."); i >= 0 {
				base = mod[i+1:]
			}
			return base, mod
		}
	case Go:
		if i := strings.LastIndex(target, "/"); i >= 0 {
			pkg = target
			alias = target[i+1:]
		} else {
			pkg, alias = target, target
		}
		return alias, pkg
	case TypeScript, JavaScript:
		// Extract the module path from import statements.
		if strings.HasPrefix(target, "import ") {
			if i := strings.LastIndex(target, `"`); i > 0 {
				pkg = target[:i]
				if j := strings.LastIndex(pkg, `"`); j >= 0 {
					pkg = pkg[j+1:]
				}
				return pkg, pkg
			}
			if i := strings.LastIndex(target, "'"); i > 0 {
				pkg = target[:i]
				if j := strings.LastIndex(pkg, "'"); j >= 0 {
					pkg = pkg[j+1:]
				}
				return pkg, pkg
			}
		}
		// require("z") or "./helper"
		target = strings.Trim(target, `"'`)
		// Strip query/hash suffixes
		if i := strings.IndexAny(target, "?#"); i >= 0 {
			target = target[:i]
		}
		if i := strings.LastIndex(target, "/"); i >= 0 {
			alias = target[i+1:]
		} else {
			alias = target
		}
		alias = strings.TrimSuffix(alias, filepath.Ext(alias))
		return alias, target
	}
	return target, target
}

var _ = filepath.Base

func grammarFor(lang Language) (*tree_sitter.Language, bool) {
	switch lang {
	case Python:
		return tree_sitter.NewLanguage(tspy.Language()), true
	case TypeScript:
		return tree_sitter.NewLanguage(tsts.LanguageTypescript()), true
	case JavaScript:
		return tree_sitter.NewLanguage(tsjs.Language()), true
	case Go:
		return tree_sitter.NewLanguage(tsgo.Language()), true
	case HCL:
		return tree_sitter.NewLanguage(tshcl.Language()), true
	default:
		return nil, false
	}
}

// walk visits every node in the tree.
func walk(n *tree_sitter.Node, content []byte, fn func(*tree_sitter.Node)) {
	fn(n)
	for i := uint(0); i < n.NamedChildCount(); i++ {
		walk(n.NamedChild(i), content, fn)
	}
}

// definitionFor returns a Symbol if n is a definition node for the language.
func definitionFor(lang Language, n *tree_sitter.Node, content []byte) (Symbol, bool) {
	kind := n.Kind()
	pos := n.StartPosition()
	line := int(pos.Row) + 1
	col := int(pos.Column)
	endLine := int(n.EndPosition().Row) + 1

	switch lang {
	case Python:
		switch kind {
		case "function_definition":
			if name := childText(n, "name", content); name != "" {
				return Symbol{Name: name, Kind: "function", Line: line, Col: col, EndLine: endLine}, true
			}
		case "class_definition":
			if name := childText(n, "name", content); name != "" {
				return Symbol{Name: name, Kind: "class", Line: line, Col: col, EndLine: endLine}, true
			}
		case "decorated_definition":
			if inner := n.NamedChild(0); inner != nil {
				if sym, ok := definitionFor(lang, inner, content); ok {
					return sym, true
				}
			}
		}
	case TypeScript, JavaScript:
		switch kind {
		case "function_declaration":
			if name := childText(n, "name", content); name != "" {
				return Symbol{Name: name, Kind: "function", Line: line, Col: col, EndLine: endLine}, true
			}
		case "class_declaration":
			if name := childText(n, "name", content); name != "" {
				return Symbol{Name: name, Kind: "class", Line: line, Col: col, EndLine: endLine}, true
			}
		case "method_definition":
			if name := childText(n, "name", content); name != "" {
				return Symbol{Name: name, Kind: "method", Line: line, Col: col, EndLine: endLine}, true
			}
		case "interface_declaration":
			if name := childText(n, "name", content); name != "" {
				return Symbol{Name: name, Kind: "interface", Line: line, Col: col, EndLine: endLine}, true
			}
		case "type_alias_declaration":
			if name := childText(n, "name", content); name != "" {
				return Symbol{Name: name, Kind: "type", Line: line, Col: col, EndLine: endLine}, true
			}
		case "lexical_declaration", "variable_declaration":
			// const foo = ... / let foo = ... — capture top-level named bindings.
			if n.Parent() != nil && n.Parent().Kind() == "program" {
				for i := uint(0); i < n.NamedChildCount(); i++ {
					c := n.NamedChild(i)
					if c.Kind() == "variable_declarator" {
						if name := childText(c, "name", content); name != "" {
							return Symbol{Name: name, Kind: "variable", Line: line, Col: col, EndLine: endLine}, true
						}
					}
				}
			}
		}
	case Go:
		switch kind {
		case "function_declaration":
			if name := childText(n, "name", content); name != "" {
				return Symbol{Name: name, Kind: "function", Line: line, Col: col, EndLine: endLine}, true
			}
		case "method_declaration":
			if name := childText(n, "name", content); name != "" {
				return Symbol{Name: name, Kind: "method", Line: line, Col: col, EndLine: endLine}, true
			}
		case "type_declaration":
			for i := uint(0); i < n.NamedChildCount(); i++ {
				c := n.NamedChild(i)
				if c.Kind() == "type_spec" {
					if name := childText(c, "name", content); name != "" {
						return Symbol{Name: name, Kind: "type", Line: line, Col: col, EndLine: endLine}, true
					}
				}
			}
		}
	case HCL:
		switch kind {
		case "block":
			// resource "aws_instance" "web" { ... } — the block type is the
			// first identifier child.
			if n.NamedChildCount() > 0 {
				first := n.NamedChild(0)
				if first.Kind() == "identifier" {
					name := first.Utf8Text(content)
					return Symbol{Name: name, Kind: "block", Line: line, Col: col, EndLine: endLine}, true
				}
			}
		}
	}
	return Symbol{}, false
}

// childText returns the text of a named child by field name.
func childText(n *tree_sitter.Node, field string, content []byte) string {
	c := n.ChildByFieldName(field)
	if c == nil {
		return ""
	}
	return c.Utf8Text(content)
}

// summarize builds a one-line summary from the extracted symbols.
func summarize(lang Language, symbols []Symbol) string {
	if len(symbols) == 0 {
		return ""
	}
	var parts []string
	for _, s := range symbols {
		parts = append(parts, s.Name)
	}
	if len(parts) > 8 {
		parts = parts[:8]
		parts = append(parts, "...")
	}
	return strings.Join(parts, ", ")
}
