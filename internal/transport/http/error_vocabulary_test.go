package http

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// Issue #47: every error this transport writes goes out through one of the
// writeError* wrappers, and each takes the same two protocol words — the error
// `type`, which the schema constrains to a six-value enum, and the `code`,
// which Errors §3 requires to be either one of the specification's or
// namespaced with a vendor prefix.
//
// Nothing checked either. #47 was filed against three sites in internal/service
// and a review of its fix turned up two more here that no one had noticed: an
// error type spelled as a bare `"server_error"` string, and `streaming_
// unsupported` — an addition to the specification's list wearing no prefix, so
// a future UHP version defining that name would collide with this server's
// meaning for it.
//
// Finding them by reading is what this replaces. A call site is not reachable
// from a test without provoking the exact condition that raises it — a response
// writer that cannot flush, among others — so the vocabulary is checked where
// it is written rather than where it is sent, by reading this package's own
// source.
//
// What it deliberately does not require is that a code be a named constant.
// This package spells the specification's codes as literals and its own as
// vendorCode* constants, and that split is intended: a literal that matches the
// specification is self-evidently right, where an addition needs the prefix
// argued for beside it.

// writeErrorFuncs are the wrappers that put an error object on the wire. Each
// takes (w, status, errType, code, …), so the two words are always at these
// argument positions.
var writeErrorFuncs = map[string]bool{
	"writeError":           true,
	"writeErrorFull":       true,
	"writeErrorParam":      true,
	"writeErrorDetail":     true,
	"writeErrorRetryAfter": true,
}

const (
	argErrType = 2
	argCode    = 3
)

// constStrings collects every package-level string constant, so an argument
// written as an identifier can be resolved to the value it will actually send.
func constStrings(t *testing.T, pkg *ast.Package) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range pkg.Files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if s, ok := stringValue(vs.Values[i], nil); ok {
						out[name.Name] = s
					}
				}
			}
		}
	}
	return out
}

// stringValue resolves an expression to the string it will carry, if it can.
//
// It handles the three forms this package uses — a literal, a local constant,
// and a qualified one such as uhp.CodeHarnessError — and reports false for
// anything else. A caller treats "cannot resolve" as "not checkable", not as a
// failure: an error whose code is genuinely computed is a legitimate shape and
// this test is not the place to reject it.
func stringValue(e ast.Expr, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.Ident:
		s, ok := consts[v.Name]
		return s, ok
	case *ast.SelectorExpr:
		// uhp.CodeX / uhpgo.CodeX. The qualified constants are the published
		// ones and are checked in their own packages, so resolving them here
		// only needs the names this package actually reaches for.
		s, ok := qualifiedConsts[exprString(v)]
		return s, ok
	}
	return "", false
}

func exprString(s *ast.SelectorExpr) string {
	pkg, ok := s.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return pkg.Name + "." + s.Sel.Name
}

// qualifiedConsts is the published vocabulary this package reaches for by
// selector. It is short on purpose: a name added here is a name a call site
// started using, and the compiler catches a misspelling of it.
var qualifiedConsts = map[string]string{
	"uhp.ErrorTypeInvalidRequest": uhp.ErrorTypeInvalidRequest,
	"uhp.ErrorTypeAuthentication": uhp.ErrorTypeAuthentication,
	"uhp.ErrorTypePermission":     uhp.ErrorTypePermission,
	"uhp.ErrorTypeRateLimit":      uhp.ErrorTypeRateLimit,
	"uhp.ErrorTypeHarness":        uhp.ErrorTypeHarness,
	"uhp.ErrorTypeServerError":    uhp.ErrorTypeServerError,
	"uhp.CodeHarnessError":        uhp.CodeHarnessError,
}

// schemaVocabulary reads the six error types and the specification's code list
// off the vendored normative schema, rather than restating either here.
//
// The types are an `enum` and the codes are `examples`, and that difference is
// the whole reason the code rule has to be expressed in Go: the list is open by
// design, because a server MAY add to it. The prefix is the condition attached
// to that permission.
func schemaVocabulary(t *testing.T) (types map[string]bool, codes map[string]bool) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(uhp.SchemaJSON), &doc); err != nil {
		t.Fatalf("decode vendored schema: %v", err)
	}
	defs, _ := doc["$defs"].(map[string]any)
	errDef, _ := defs["Error"].(map[string]any)
	props, _ := errDef["properties"].(map[string]any)

	typeProp, _ := props["type"].(map[string]any)
	enum, _ := typeProp["enum"].([]any)
	codeProp, _ := props["code"].(map[string]any)
	examples, _ := codeProp["examples"].([]any)
	if len(enum) == 0 || len(examples) == 0 {
		t.Fatal("the vendored schema carries no Error.type enum or Error.code examples; this test reads the vocabulary from there")
	}

	set := func(vals []any) map[string]bool {
		m := make(map[string]bool, len(vals))
		for _, v := range vals {
			s, _ := v.(string)
			m[s] = true
		}
		return m
	}
	return set(enum), set(examples)
}

func TestEveryErrorWrittenUsesTheProtocolVocabulary(t *testing.T) {
	validTypes, specCodes := schemaVocabulary(t)

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	pkg, ok := pkgs["http"]
	if !ok {
		t.Fatal("package http did not parse")
	}
	consts := constStrings(t, pkg)

	var checked int
	for _, f := range pkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || !writeErrorFuncs[fn.Name] || len(call.Args) <= argCode {
				return true
			}
			pos := fset.Position(call.Pos())
			checked++

			if got, ok := stringValue(call.Args[argErrType], consts); ok && !validTypes[got] {
				t.Errorf("%s: %s writes error type %q, which is not one of the schema's six; a client that meets an unfamiliar code falls back to this field and cannot classify it",
					pos, fn.Name, got)
			}
			if got, ok := stringValue(call.Args[argCode], consts); ok {
				if !specCodes[got] && !strings.HasPrefix(got, uhpgo.CodePrefix) {
					t.Errorf("%s: %s writes error code %q, which is neither one of the specification's nor namespaced with %q; an addition MUST be prefixed so a future UHP version cannot collide with it",
						pos, fn.Name, got, uhpgo.CodePrefix)
				}
			}
			return true
		})
	}

	// A guard that silently checks nothing is worse than no guard, and this one
	// depends on wrapper names and argument positions that a refactor can move.
	if checked < 20 {
		t.Errorf("only %d error-writing call sites were found; the wrappers or their argument order have moved and this test is no longer reading them", checked)
	}
}
