package guard

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const importsFile = "internal/guard/imports.json"

// importTable is imports.json: the allowed internal graph per package, and
// the packages only named importers may reach.
type importTable struct {
	Modules map[string]moduleRule `json:"modules"`
	OnlyBy  map[string]onlyByRule `json:"only_by"`
}

type moduleRule struct {
	MayUse     []string   `json:"may_use"`
	MustNotUse []denyRule `json:"must_not_use,omitempty"`
}

type denyRule struct {
	Module string `json:"module"`
	Reason string `json:"reason"`
}

type onlyByRule struct {
	Modules []string `json:"modules"`
	Reason  string   `json:"reason"`
}

type edge struct {
	from, to string
	file     string
	line     int
	internal bool
}

// CheckBoundaries walks the production Go files under cmd/ and internal/ and
// reports every import that imports.json does not allow, every deny rule an
// import breaks, and every table entry that no longer describes the code. The
// table must describe the code as it is: an unlisted edge and a stale entry
// both fail, so a dependency change is always a visible diff.
func CheckBoundaries(root string, config []byte) []string {
	var table importTable
	if err := decodeConfig(importsFile, config, &table); err != nil {
		return []string{err.Error()}
	}
	mod, err := modulePath(root)
	if err != nil {
		return []string{importsFile + ": " + err.Error()}
	}
	packages, edges, bad := readGraph(root, mod)

	// Resolve one rule per package, so wildcard and exact entries cannot both
	// apply to the same code by accident.
	rules := make(map[string]moduleRule, len(packages))
	for _, pkg := range packages {
		key, rule, problems := resolveRule(table.Modules, pkg)
		bad = append(bad, problems...)
		if key == "" {
			continue
		}
		rules[pkg] = rule
	}

	for key := range table.Modules {
		if !matchesPackage(packages, key) {
			bad = append(bad, fmt.Sprintf("%s: modules[%q] matches no package under cmd/ or internal/; delete that entry", importsFile, key))
		}
	}

	targets := make(map[string]bool)
	for _, e := range edges {
		targets[e.to] = true
	}
	for _, rule := range table.Modules {
		for _, deny := range rule.MustNotUse {
			if strings.TrimSpace(deny.Reason) == "" {
				bad = append(bad, fmt.Sprintf("%s: must_not_use %q has no reason; every deny rule says why it exists", importsFile, deny.Module))
			}
			if !matchesPackage(packages, deny.Module) {
				bad = append(bad, fmt.Sprintf("%s: must_not_use %q names no package under cmd/ or internal/; delete it", importsFile, deny.Module))
			}
		}
	}
	for target, only := range table.OnlyBy {
		if strings.TrimSpace(only.Reason) == "" {
			bad = append(bad, fmt.Sprintf("%s: only_by %q has no reason; every deny rule says why it exists", importsFile, target))
		}
		if len(only.Modules) > 0 && !matchesPackage(packages, target) && !matchesPackage(mapKeys(targets), target) {
			bad = append(bad, fmt.Sprintf("%s: only_by %q names nothing this tree imports; delete it", importsFile, target))
		}
	}

	if len(edges) == 0 {
		bad = append(bad, fmt.Sprintf("%s: found no imports under cmd/ or internal/; the scanner is broken or the tree is empty, so nothing was checked", importsFile))
	}

	seen := make(map[string]bool)
	for _, e := range edges {
		at := fmt.Sprintf("%s:%d", e.file, e.line)
		if rule, ok := rules[e.from]; ok {
			if deny, hit := findDeny(rule.MustNotUse, e.to); hit {
				bad = append(bad, fmt.Sprintf("%s: %s may not import %s (%s): %s", at, e.from, e.to, importsFile, deny.Reason))
				continue
			}
		}
		denied := false
		for target, only := range table.OnlyBy {
			if !match(target, e.to) || matchAny(only.Modules, e.from) {
				continue
			}
			bad = append(bad, fmt.Sprintf("%s: only %s may import %s (%s): %s", at, strings.Join(orAll(only.Modules), ", "), e.to, importsFile, only.Reason))
			denied = true
		}
		if denied || !e.internal {
			continue
		}
		rule, ok := rules[e.from]
		if !ok || matchAny(rule.MayUse, e.to) {
			continue
		}
		pair := e.from + " -> " + e.to
		if seen[pair] {
			continue
		}
		seen[pair] = true
		bad = append(bad, fmt.Sprintf("%s: %s uses %s, which %s does not list; add %q to may_use under modules[%q] so the dependency shows in the diff", at, e.from, e.to, importsFile, e.to, e.from))
	}
	sort.Strings(bad)
	return bad
}

// readGraph returns the packages the gate knows about, every import edge
// between them (or from them to an external path), and file-level problems.
func readGraph(root, mod string) ([]string, []edge, []string) {
	var bad []string
	files, err := goFiles(root, false)
	if err != nil {
		return nil, nil, []string{err.Error()}
	}
	var packages []string
	var edges []edge
	fset := token.NewFileSet()
	known := make(map[string]bool)
	for _, file := range files {
		pkg := path.Dir(file)
		if !known[pkg] {
			known[pkg] = true
			packages = append(packages, pkg)
		}
		src, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		f, err := parser.ParseFile(fset, file, src, parser.ImportsOnly)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		for _, spec := range f.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				bad = append(bad, fmt.Sprintf("%s:%d: bad import path %s", file, fset.Position(spec.Pos()).Line, spec.Path.Value))
				continue
			}
			e := edge{from: pkg, to: imported, file: file, line: fset.Position(spec.Pos()).Line}
			if rel, ok := strings.CutPrefix(imported, mod+"/"); ok {
				if rel == pkg {
					continue
				}
				e.to, e.internal = rel, true
			}
			edges = append(edges, e)
		}
	}
	sort.Strings(packages)
	return packages, edges, bad
}

// resolveRule picks the one entry that governs pkg: an exact key wins over a
// wildcard, and the most specific wildcard wins over broader ones.
func resolveRule(modules map[string]moduleRule, pkg string) (string, moduleRule, []string) {
	if rule, ok := modules[pkg]; ok {
		return pkg, rule, nil
	}
	best, bestLen := "", -1
	var tie []string
	for key := range modules {
		if !match(key, pkg) {
			continue
		}
		n := len(strings.SplitN(key, "*", 2)[0])
		switch {
		case n > bestLen:
			best, bestLen, tie = key, n, []string{key}
		case n == bestLen:
			tie = append(tie, key)
		}
	}
	if bestLen < 0 {
		return "", moduleRule{}, []string{fmt.Sprintf("%s: %s has no entry; add modules[%q] with the internal packages it may use in may_use", importsFile, pkg, pkg)}
	}
	if len(tie) > 1 {
		sort.Strings(tie)
		return "", moduleRule{}, []string{fmt.Sprintf("%s: %s matches %s equally well; keep one of them", importsFile, pkg, strings.Join(tie, ", "))}
	}
	return best, modules[best], nil
}

func findDeny(rules []denyRule, target string) (denyRule, bool) {
	for _, r := range rules {
		if match(r.Module, target) {
			return r, true
		}
	}
	return denyRule{}, false
}

// matchesPackage reports whether any real package matches pattern; the
// inverse direction of matchAny, used to keep table entries honest.
func matchesPackage(packages []string, pattern string) bool {
	for _, pkg := range packages {
		if match(pattern, pkg) {
			return true
		}
	}
	return false
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func orAll(patterns []string) []string {
	if len(patterns) == 0 {
		return []string{"nothing"}
	}
	return patterns
}
