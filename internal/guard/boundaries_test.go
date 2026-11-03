package guard

import "testing"

const cleanGraph = `{
  "modules": {
    "internal/model": {"may_use": []},
    "internal/project": {"may_use": ["internal/model"]},
    "internal/cli": {"may_use": ["internal/*"]},
    "cmd/chatstrata": {"may_use": ["internal/cli"]}
  },
  "only_by": {
    "internal/cli": {"modules": ["cmd/chatstrata"], "reason": "the CLI package holds command wiring"}
  }
}`

func TestBoundariesClean(t *testing.T) {
	root := writeTree(t, map[string]string{
		"internal/model/model.go":     "package model\n",
		"internal/project/project.go": "package project\n\nimport \"example.com/m/internal/model\"\n\nvar _ = model.Name\n",
		"internal/cli/cli.go":         "package cli\n",
		"cmd/chatstrata/main.go":      "package main\n\nimport \"example.com/m/internal/cli\"\n\nfunc main() { cli.Run() }\n",
	})
	wantClean(t, CheckBoundaries(root, []byte(cleanGraph)))
}

func TestBoundariesRejectsCLIOutsideCmd(t *testing.T) {
	root := writeTree(t, map[string]string{
		"internal/model/model.go":     "package model\n",
		"internal/project/project.go": "package project\n\nimport \"example.com/m/internal/cli\"\n\nvar _ = cli.Run\n",
		"internal/cli/cli.go":         "package cli\n",
		"cmd/chatstrata/main.go":      "package main\n\nimport \"example.com/m/internal/cli\"\n\nfunc main() { cli.Run() }\n",
	})
	problems := CheckBoundaries(root, []byte(cleanGraph))
	wantProblems(t, problems, "internal/project/project.go:3", "only cmd/chatstrata may import internal/cli", "command wiring")
}

func TestBoundariesRejectsUnlistedEdge(t *testing.T) {
	cfg := `{
  "modules": {
    "internal/model": {"may_use": []},
    "internal/project": {"may_use": []}
  }
}`
	root := writeTree(t, map[string]string{
		"internal/model/model.go":     "package model\n",
		"internal/project/project.go": "package project\n\nimport \"example.com/m/internal/model\"\n\nvar _ = model.Name\n",
	})
	wantProblems(t, CheckBoundaries(root, []byte(cfg)),
		"internal/project uses internal/model",
		`add "internal/model" to may_use under modules["internal/project"]`,
	)
}

func TestBoundariesRejectsDenyWithoutReason(t *testing.T) {
	cfg := `{
  "modules": {
    "internal/cli": {"may_use": []},
    "internal/project": {"may_use": ["internal/*"], "must_not_use": [{"module": "internal/cli", "reason": " "}]}
  }
}`
	root := writeTree(t, map[string]string{
		"internal/cli/cli.go":         "package cli\n",
		"internal/project/project.go": "package project\n",
	})
	wantProblems(t, CheckBoundaries(root, []byte(cfg)), `must_not_use "internal/cli" has no reason`)
}

func TestBoundariesRejectsStaleEntry(t *testing.T) {
	cfg := `{
  "modules": {
    "internal/project": {"may_use": []},
    "internal/gone": {"may_use": []}
  }
}`
	root := writeTree(t, map[string]string{
		"internal/project/project.go": "package project\n",
	})
	wantProblems(t, CheckBoundaries(root, []byte(cfg)), `modules["internal/gone"] matches no package`)
}

func TestBoundariesRejectsAmbiguousWildcards(t *testing.T) {
	cfg := `{
  "modules": {
    "internal/a*b*": {"may_use": []},
    "internal/a*c*": {"may_use": []}
  }
}`
	root := writeTree(t, map[string]string{
		"internal/abc/x.go": "package abc\n",
	})
	wantProblems(t, CheckBoundaries(root, []byte(cfg)), "matches internal/a*b*, internal/a*c* equally well")
}

func TestBoundariesRejectsEmptyGraph(t *testing.T) {
	cfg := `{"modules": {"internal/model": {"may_use": []}}}`
	root := writeTree(t, map[string]string{
		"internal/model/model.go": "package model\n",
	})
	wantProblems(t, CheckBoundaries(root, []byte(cfg)), "found no imports")
}
