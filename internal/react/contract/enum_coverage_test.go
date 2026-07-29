package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestPolicyEnumRegistriesCoverTypedConstants(t *testing.T) {
	// Stage 0A treats only control states and model output kinds as closed
	// policy axes. Domain enums are intentionally outside this registry test.
	tests := []struct {
		name     string
		typeName string
		registry []string
	}{
		{
			name:     "runtime control states",
			typeName: "RuntimeControlState",
			registry: controlStateStrings(AllRuntimeControlStates()),
		},
		{
			name:     "model output kinds",
			typeName: "ModelOutputKind",
			registry: outputKindStrings(AllModelOutputKinds()),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			declared := declaredStringConstants(t, tt.typeName)
			assertSameStringSet(t, declared, tt.registry)
		})
	}
}

func TestRuntimeControlClassificationIsComplete(t *testing.T) {
	counts := map[ControlExecutionClass]int{}
	for _, state := range AllRuntimeControlStates() {
		class, ok := ClassifyRuntimeControlState(state)
		if !ok {
			t.Fatalf("state %q is unclassified", state)
		}
		counts[class]++
	}
	if got := counts[ControlExecutionInvalid]; got != 1 {
		t.Fatalf("invalid states = %d, want 1", got)
	}
	if got := counts[ControlExecutionModelTurn]; got != 14 {
		t.Fatalf("model-turn states = %d, want 14", got)
	}
	if got := counts[ControlExecutionNonModelTurn]; got != 6 {
		t.Fatalf("non-model-turn states = %d, want 6", got)
	}
	if _, ok := ClassifyRuntimeControlState("future_state"); ok {
		t.Fatal("unknown control state must not be classified")
	}
}

func declaredStringConstants(t *testing.T, typeName string) []string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	dir := filepath.Dir(currentFile)
	packages, err := parser.ParseDir(token.NewFileSet(), dir, func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse contract package: %v", err)
	}
	pkg := packages["contract"]
	if pkg == nil {
		t.Fatal("contract package not found")
	}
	var values []string
	for _, file := range pkg.Files {
		for _, rawDeclaration := range file.Decls {
			declaration, ok := rawDeclaration.(*ast.GenDecl)
			if !ok || declaration.Tok != token.CONST {
				continue
			}
			for _, rawSpec := range declaration.Specs {
				spec, ok := rawSpec.(*ast.ValueSpec)
				if !ok || !isNamedType(spec.Type, typeName) {
					continue
				}
				if len(spec.Values) != len(spec.Names) {
					t.Fatalf("%s declaration must give every constant an explicit value", typeName)
				}
				for _, expression := range spec.Values {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						t.Fatalf("%s declaration must use string literals", typeName)
					}
					value, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatalf("decode %s constant: %v", typeName, err)
					}
					values = append(values, value)
				}
			}
		}
	}
	return values
}

func isNamedType(expression ast.Expr, typeName string) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == typeName
}

func assertSameStringSet(t *testing.T, declared, registered []string) {
	t.Helper()
	declaredSet := uniqueStringSet(t, "declared", declared)
	registeredSet := uniqueStringSet(t, "registered", registered)
	for value := range declaredSet {
		if _, ok := registeredSet[value]; !ok {
			t.Errorf("declared value %q is missing from registry", value)
		}
	}
	for value := range registeredSet {
		if _, ok := declaredSet[value]; !ok {
			t.Errorf("registry value %q has no typed constant", value)
		}
	}
}

func uniqueStringSet(t *testing.T, label string, values []string) map[string]struct{} {
	t.Helper()
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := result[value]; duplicate {
			t.Fatalf("%s value %q is duplicated", label, value)
		}
		result[value] = struct{}{}
	}
	return result
}

func controlStateStrings(states []RuntimeControlState) []string {
	result := make([]string, len(states))
	for i, state := range states {
		result[i] = string(state)
	}
	return result
}

func outputKindStrings(kinds []ModelOutputKind) []string {
	result := make([]string, len(kinds))
	for i, kind := range kinds {
		result[i] = string(kind)
	}
	return result
}
