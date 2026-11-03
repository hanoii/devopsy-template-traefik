package tests

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot is the repository's root, from this package's directory.
const repoRoot = ".."

// rootDoc is the agents' root file, from the repository's root.
const rootDoc = "AGENTS.new.md"

// Every path, shell word and Go name the agents' rules cite in code
// spans exists: a rename or a removal that leaves the rules behind fails
// here. No build tag, so it needs no Docker. The histories are left out:
// they tell of names that went on purpose. What is ambiguous (a
// variable, a label, a value, a path on a server or in mnt/) is left out
// too: a check that misses a name costs less than one that cries wolf.
func TestAgentsNamesWhatExists(t *testing.T) {
	t.Parallel()
	docs := []string{rootDoc}
	areas, _ := filepath.Glob(filepath.Join(repoRoot, "internals", "*.md"))
	for _, a := range areas {
		if !strings.HasSuffix(a, ".history.md") {
			docs = append(docs, strings.TrimPrefix(filepath.ToSlash(a), repoRoot+"/"))
		}
	}
	files, words, goNames := repoNames(t)
	for _, doc := range docs {
		data, err := os.ReadFile(filepath.Join(repoRoot, doc))
		if err != nil {
			t.Fatal(err)
		}
		for _, span := range codeSpans(string(data)) {
			switch {
			case isPath(span):
				if !files.has(strings.TrimSuffix(span, "/")) {
					t.Errorf("%s names %s, no file or directory of the repository", doc, span)
				}
			case shellWord.MatchString(span):
				if !words[span] && !goNames[span] {
					t.Errorf("%s names %s, named nowhere in .devopsy/ or the Go source", doc, span)
				}
			case goName.MatchString(span):
				name := strings.TrimSuffix(span, "()")
				if i := strings.LastIndex(name, "."); i >= 0 {
					name = name[i+1:]
				}
				if !goNames[name] {
					t.Errorf("%s names %s, declared nowhere in the Go source", doc, span)
				}
			}
		}
	}
}

// codeSpans are a markdown file's inline code spans, outside fenced
// blocks.
func codeSpans(md string) []string {
	var spans []string
	fenced := false
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(line, -1) {
			spans = append(spans, m[1])
		}
	}
	return spans
}

var (
	// A path: a directory of the repository's, of .devopsy/'s, or a name
	// with an extension; `...` stands for any directories between.
	pathSpan = regexp.MustCompile(`^(\.devopsy/|commands/|lib/|plugins-local/|tests/|internals/|\.github/)[A-Za-z0-9_./-]*$|^\.?[A-Za-z0-9_-][A-Za-z0-9_.-]*(/[A-Za-z0-9_.-]+)*\.(go|md|mod|yml|yaml|sh|example|jq)$`)
	// A shell function's or a compose key's name: lowercase, with an
	// underscore.
	shellWord = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)+$`)
	// A Go name: CamelCase of two humps at least, or a method (`h.routed`).
	goName = regexp.MustCompile(`^(?:[A-Z][a-z0-9]+(?:[A-Z][a-z0-9]*)+|[a-z][a-z0-9]*\.[a-z][A-Za-z0-9]*)(?:\(\))?$`)
)

// isPath says whether a span is a path of the repository's. The
// runtime's (mnt/, shared/, a server's) are none.
func isPath(span string) bool {
	for _, p := range []string{"mnt/", ".devopsy/mnt", "shared/"} {
		if strings.HasPrefix(span, p) {
			return false
		}
	}
	return pathSpan.MatchString(span)
}

// paths are the repository's files and directories, slash separated
// from its root.
type paths map[string]bool

// has says whether p is a file or directory from the root or from
// .devopsy/, or the end of one's path (`hold_test.go`, `ci.yml`).
func (f paths) has(p string) bool {
	re := regexp.MustCompile(`(^|/)` + strings.ReplaceAll(regexp.QuoteMeta(p), `/\.\.\./`, `/(.+/)?`) + `$`)
	for name := range f {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

// repoNames are the repository's paths, every word of .devopsy/'s files
// and every identifier of the Go source (the plugins' and the
// tests', and every word of their strings), comments left out.
func repoNames(t *testing.T) (paths, map[string]bool, map[string]bool) {
	t.Helper()
	files, words, goNames := paths{}, map[string]bool{}, map[string]bool{}
	ident := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	err := filepath.WalkDir(repoRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".worktrees" || d.Name() == "mnt") {
			return filepath.SkipDir
		}
		rel := strings.TrimPrefix(filepath.ToSlash(p), repoRoot+"/")
		files[rel] = true
		if d.IsDir() {
			return nil
		}
		devopsy := strings.HasPrefix(rel, ".devopsy/")
		if !devopsy && !strings.HasSuffix(rel, ".go") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if devopsy {
			for _, w := range ident.FindAllString(string(data), -1) {
				words[w] = true
			}
			if !strings.HasSuffix(rel, ".go") {
				return nil
			}
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, data, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				goNames[x.Name] = true
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					for _, w := range ident.FindAllString(x.Value, -1) {
						goNames[w] = true
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files, words, goNames
}
