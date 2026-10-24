package model

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The dependency rule (Norma 5.3.1): the domain imports only the standard
// library, and never the web, persistence or messaging packages in it.
func TestDomainImportsOnlyAllowedStandardPackages(t *testing.T) {
	forbidden := []string{"net/http", "database/sql", "go.mongodb.org", "github.com/", "log/slog"}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range forbidden {
				if strings.HasPrefix(path, bad) {
					t.Errorf("%s imports %s", f, path)
				}
			}
		}
	}
}
