package guard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const allowFile = "internal/guard/allow.json"

// Escape hatches: constructs that bypass the normal seam and therefore need a
// reason before they may exist at all.
const (
	patternPanic  = "panic"
	patternStdout = "os.Stdout"
	patternStderr = "os.Stderr"
	patternExit   = "os.Exit"
	patternFatal  = "log.Fatal"
	patternTODO   = "TODO"
	patternFIXME  = "FIXME"
	patternHACK   = "HACK"
	patternXXX    = "XXX"
)

var codeHatchers = map[string]string{
	patternStdout: "CLI output must write through env.stdout so cli.Run tests can capture it",
	patternStderr: "CLI diagnostics must write through env.stderr so cli.Run tests can capture them",
	patternExit:   "only the process entry point exits; everything below returns an error or a status code",
	patternFatal:  "only the process entry point exits; log.Fatal skips deferred cleanup",
	patternPanic:  "return an error instead; a panic turns a user mistake into a crash",
}

var commentHatcher = regexp.MustCompile(`\b(TODO|FIXME|HACK|XXX)\b`)

// allowConfig is allow.json: the escape hatches that are justified, each with
// the reason it is safe.
type allowConfig struct {
	Allow []allowEntry `json:"allow"`
}

type allowEntry struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
	Reason  string `json:"reason"`
}

type hit struct {
	path    string
	pattern string
	line    int
}

// CheckAllow scans production Go files under cmd/ and internal/ for escape
// hatches and standing comment markers, and reports every hit that
// allow.json does not justify. An entry must carry a reason and match at least
// one hit, so the allowlist cannot quietly outlive the code it excuses.
func CheckAllow(root string, config []byte) []string {
	var cfg allowConfig
	if err := decodeConfig(allowFile, config, &cfg); err != nil {
		return []string{err.Error()}
	}
	var bad []string
	valid := make([]bool, len(cfg.Allow))
	for i, entry := range cfg.Allow {
		if entry.Path == "" || entry.Pattern == "" {
			bad = append(bad, fmt.Sprintf("%s: allow[%d] needs both path and pattern", allowFile, i))
			continue
		}
		if !isHatch(entry.Pattern) {
			bad = append(bad, fmt.Sprintf("%s: allow[%d] names unknown pattern %q; use one of %s", allowFile, i, entry.Pattern, strings.Join(hatchNames(), ", ")))
			continue
		}
		if strings.TrimSpace(entry.Reason) == "" {
			bad = append(bad, fmt.Sprintf("%s: allow[%d] (%s in %s) has no reason; an escape hatch must say why it is safe", allowFile, i, entry.Pattern, entry.Path))
			continue
		}
		valid[i] = true
	}

	hits, problems := scanHatches(root)
	bad = append(bad, problems...)
	used := make([]bool, len(cfg.Allow))
	for _, h := range hits {
		excused := false
		for i, entry := range cfg.Allow {
			if !valid[i] || entry.Pattern != h.pattern || !match(entry.Path, h.path) {
				continue
			}
			used[i], excused = true, true
		}
		if excused {
			continue
		}
		bad = append(bad, fmt.Sprintf("%s:%d: %s is an escape hatch; %s. If this one is justified, add it to %s with path %q, pattern %q and a reason", h.path, h.line, h.pattern, advice(h.pattern), allowFile, h.path, h.pattern))
	}
	for i, entry := range cfg.Allow {
		if valid[i] && !used[i] {
			bad = append(bad, fmt.Sprintf("%s: allow[%d] (%s in %s) matches nothing; delete it", allowFile, i, entry.Pattern, entry.Path))
		}
	}
	sort.Strings(bad)
	return bad
}

// scanHatches parses every production Go file and returns its code and comment
// hits in path order, plus any file that would not parse.
func scanHatches(root string) ([]hit, []string) {
	files, err := goFiles(root, false)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var hits []hit
	var bad []string
	fset := token.NewFileSet()
	for _, file := range files {
		src, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		f, err := parser.ParseFile(fset, file, src, parser.ParseComments)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok && id.Name == patternPanic {
					hits = append(hits, hit{file, patternPanic, fset.Position(n.Pos()).Line})
				}
			case *ast.SelectorExpr:
				if id, ok := n.X.(*ast.Ident); ok {
					key := id.Name + "." + n.Sel.Name
					if _, ok := codeHatchers[key]; ok {
						hits = append(hits, hit{file, key, fset.Position(n.Pos()).Line})
					}
				}
			}
			return true
		})
		for _, group := range f.Comments {
			for _, comment := range group.List {
				start := fset.Position(comment.Slash).Line
				for _, loc := range commentHatcher.FindAllStringIndex(comment.Text, -1) {
					line := start + strings.Count(comment.Text[:loc[0]], "\n")
					hits = append(hits, hit{file, comment.Text[loc[0]:loc[1]], line})
				}
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].path != hits[j].path {
			return hits[i].path < hits[j].path
		}
		return hits[i].line < hits[j].line
	})
	return hits, bad
}

func isHatch(pattern string) bool {
	if _, ok := codeHatchers[pattern]; ok {
		return true
	}
	return pattern == patternTODO || pattern == patternFIXME || pattern == patternHACK || pattern == patternXXX
}

func hatchNames() []string {
	names := make([]string, 0, len(codeHatchers)+4)
	for name := range codeHatchers {
		names = append(names, name)
	}
	names = append(names, patternTODO, patternFIXME, patternHACK, patternXXX)
	sort.Strings(names)
	return names
}

func advice(pattern string) string {
	if text, ok := codeHatchers[pattern]; ok {
		return text
	}
	return "a standing marker becomes a copied convention; either fix it or open an issue and delete the marker"
}
