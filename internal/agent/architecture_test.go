package agent

import (
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

func TestCorePackageDoesNotImportApplicationOrAdapters(t *testing.T) {
	packages, err := parser.ParseDir(token.NewFileSet(), ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	parsed := packages["agent"]
	if parsed == nil {
		t.Fatal("agent package not found")
	}
	const internalPrefix = "github.com/lakernote/beaver-agent/internal/"
	for filename, file := range parsed.Files {
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("parse import in %s: %v", filename, err)
			}
			if strings.HasPrefix(path, internalPrefix) {
				t.Fatalf("agent core must not import application, persistence, or adapter package %q (%s)", path, filename)
			}
		}
	}
}
