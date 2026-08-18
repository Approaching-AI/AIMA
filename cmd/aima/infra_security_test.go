package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestBuildLLMClientDoesNotEnableUntrustedFleetDiscovery(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "infra.go", nil, 0)
	if err != nil {
		t.Fatalf("parse infra.go: %v", err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "buildLLMClient" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "WithDiscoverFunc" {
				t.Error("buildLLMClient must not enable unpaired mDNS inference fallback")
			}
			return true
		})
		return
	}
	t.Fatal("buildLLMClient function not found")
}
