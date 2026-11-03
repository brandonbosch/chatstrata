package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const ratchetsFile = "internal/guard/ratchets.json"

// ratchetConfig is ratchets.json: the soft ceiling, and the exact non-test
// line count of each file already known to exceed it.
type ratchetConfig struct {
	Ceiling int            `json:"ceiling"`
	Files   map[string]int `json:"files"`
}

// CheckRatchets counts non-blank lines in every production Go file under
// cmd/ and internal/. A file over the ceiling that is not baselined fails; a
// baselined file that grows fails; a baselined file that shrinks also fails
// until its baseline is lowered in the same change, so an improvement is
// locked in; a file that drops under the ceiling leaves the list. Tests are
// separate files in Go, so leaving _test.go out is exact, not a heuristic.
func CheckRatchets(root string, config []byte) []string {
	var cfg ratchetConfig
	if err := decodeConfig(ratchetsFile, config, &cfg); err != nil {
		return []string{err.Error()}
	}
	if cfg.Ceiling <= 0 {
		return []string{fmt.Sprintf("%s: ceiling must be a positive line count", ratchetsFile)}
	}
	files, err := goFiles(root, false)
	if err != nil {
		return []string{err.Error()}
	}
	over := make(map[string]int)
	var bad []string
	for _, file := range files {
		src, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		if n := countLines(string(src)); n > cfg.Ceiling {
			over[file] = n
		}
	}
	for file, n := range over {
		baseline, listed := cfg.Files[file]
		switch {
		case !listed:
			bad = append(bad, fmt.Sprintf("%s: %d non-test lines, over the %d-line ceiling; split it by concern, or if it is one concern add %q: %d under files in %s", file, n, cfg.Ceiling, file, n, ratchetsFile))
		case n > baseline:
			bad = append(bad, fmt.Sprintf("%s: %d non-test lines, %d over its baseline of %d in %s; split it by concern instead of adding to it", file, n, n-baseline, baseline, ratchetsFile))
		case n < baseline:
			bad = append(bad, fmt.Sprintf("%s: %d non-test lines, below its baseline of %d in %s; lower the baseline to %d so the improvement is locked in", file, n, baseline, ratchetsFile, n))
		}
	}
	for file := range cfg.Files {
		if _, ok := over[file]; !ok {
			bad = append(bad, fmt.Sprintf("%s: %s is no longer over the %d-line ceiling (or is gone); delete its line", ratchetsFile, file, cfg.Ceiling))
		}
	}
	sort.Strings(bad)
	return bad
}

// countLines counts non-blank lines: the unit construct's arch gate uses, and
// a better proxy for file size than raw lines that include blank separators.
func countLines(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
