package guard

import "testing"

const panicFile = "package x\n\nfunc f() { panic(\"impossible\") }\n"

func TestAllowClean(t *testing.T) {
	cfg := `{"allow": [{"path": "internal/x/x.go", "pattern": "panic", "reason": "an invariant no input can break"}]}`
	root := writeTree(t, map[string]string{"internal/x/x.go": panicFile})
	wantClean(t, CheckAllow(root, []byte(cfg)))
}

func TestAllowRejectsNewPanic(t *testing.T) {
	cfg := `{"allow": []}`
	root := writeTree(t, map[string]string{"internal/x/x.go": panicFile})
	wantProblems(t, CheckAllow(root, []byte(cfg)), "internal/x/x.go:3: panic is an escape hatch", "return an error instead")
}

func TestAllowRejectsEntryWithoutReason(t *testing.T) {
	cfg := `{"allow": [{"path": "internal/x/x.go", "pattern": "panic", "reason": ""}]}`
	root := writeTree(t, map[string]string{"internal/x/x.go": panicFile})
	wantProblems(t, CheckAllow(root, []byte(cfg)), "has no reason")
}

func TestAllowRejectsStaleEntry(t *testing.T) {
	cfg := `{"allow": [{"path": "internal/x/x.go", "pattern": "panic", "reason": "stale"}]}`
	root := writeTree(t, map[string]string{"internal/x/x.go": "package x\n"})
	wantProblems(t, CheckAllow(root, []byte(cfg)), "matches nothing; delete it")
}

func TestAllowRejectsUnknownPattern(t *testing.T) {
	cfg := `{"allow": [{"path": "internal/x/x.go", "pattern": "fmt.Println", "reason": "made up"}]}`
	root := writeTree(t, map[string]string{"internal/x/x.go": "package x\n"})
	wantProblems(t, CheckAllow(root, []byte(cfg)), `unknown pattern "fmt.Println"`)
}

func TestAllowRejectsTodoComment(t *testing.T) {
	cfg := `{"allow": []}`
	root := writeTree(t, map[string]string{"internal/x/x.go": "package x\n\n// TODO: handle the empty case\n"})
	wantProblems(t, CheckAllow(root, []byte(cfg)), "internal/x/x.go:3: TODO is an escape hatch")
}

func TestAllowMatchesPathPattern(t *testing.T) {
	cfg := `{"allow": [{"path": "internal/tools/*/main.go", "pattern": "os.Stderr", "reason": "build-time tools have no cli.Run"}]}`
	root := writeTree(t, map[string]string{
		"internal/tools/a/main.go": "package main\n\nimport \"os\"\n\nfunc main() { os.Stderr.Write(nil) }\n",
	})
	wantClean(t, CheckAllow(root, []byte(cfg)))
}

func TestAllowIgnoresStringsAndTestFiles(t *testing.T) {
	cfg := `{"allow": []}`
	root := writeTree(t, map[string]string{
		"internal/x/x.go":      "package x\n\nvar s = \"panic\"\n",
		"internal/x/x_test.go": "package x\n\nvar _ = \"TODO\"\n",
	})
	wantClean(t, CheckAllow(root, []byte(cfg)))
}
