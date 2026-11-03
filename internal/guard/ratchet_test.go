package guard

import (
	"strings"
	"testing"
)

func lines(n int) string {
	return strings.Repeat("var x = 1\n", n)
}

func TestRatchetsClean(t *testing.T) {
	cfg := `{"ceiling": 5, "files": {"internal/big/a.go": 7}}`
	root := writeTree(t, map[string]string{
		"internal/small/a.go": "package small\n",
		"internal/big/a.go":   "package big\n" + lines(6),
	})
	wantClean(t, CheckRatchets(root, []byte(cfg)))
}

func TestRatchetsRejectsNewLongFile(t *testing.T) {
	cfg := `{"ceiling": 5, "files": {}}`
	root := writeTree(t, map[string]string{
		"internal/big/a.go": "package big\n" + lines(5),
	})
	wantProblems(t, CheckRatchets(root, []byte(cfg)),
		"internal/big/a.go: 6 non-test lines",
		"over the 5-line ceiling",
	)
}

func TestRatchetsRejectsGrowth(t *testing.T) {
	cfg := `{"ceiling": 5, "files": {"internal/big/a.go": 6}}`
	root := writeTree(t, map[string]string{
		"internal/big/a.go": "package big\n" + lines(6),
	})
	wantProblems(t, CheckRatchets(root, []byte(cfg)),
		"internal/big/a.go: 7 non-test lines, 1 over its baseline of 6",
	)
}

func TestRatchetsRejectsShrinkUntilBaselined(t *testing.T) {
	cfg := `{"ceiling": 5, "files": {"internal/big/a.go": 8}}`
	root := writeTree(t, map[string]string{
		"internal/big/a.go": "package big\n" + lines(6),
	})
	wantProblems(t, CheckRatchets(root, []byte(cfg)),
		"7 non-test lines, below its baseline of 8",
		"lower the baseline to 7",
	)
}

func TestRatchetsRejectsStaleBaseline(t *testing.T) {
	cfg := `{"ceiling": 5, "files": {"internal/big/a.go": 6, "internal/gone/gone.go": 9}}`
	root := writeTree(t, map[string]string{
		"internal/big/a.go": "package big\n" + lines(5),
	})
	wantProblems(t, CheckRatchets(root, []byte(cfg)), "internal/gone/gone.go", "delete its line")
}

func TestRatchetsIgnoresTestFiles(t *testing.T) {
	cfg := `{"ceiling": 5, "files": {}}`
	root := writeTree(t, map[string]string{
		"internal/big/a_test.go": "package big\n" + lines(20),
	})
	wantClean(t, CheckRatchets(root, []byte(cfg)))
}
