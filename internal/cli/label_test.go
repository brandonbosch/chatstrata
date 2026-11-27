package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brandonbosch/chatstrata/v2/internal/label"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

func fakeCLIKey() string {
	const alpha = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	r := rand.New(rand.NewSource(11))
	var b strings.Builder
	b.WriteString("sk-proj-")
	for range 100 {
		b.WriteByte(alpha[r.Intn(len(alpha))])
	}
	return b.String()
}

func TestLabelCLIDryRunLiveRebuildAndClear(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "chatstrata-go.duckdb")

	// Write a synthetic Claude Code session containing an email + secret + tool call.
	projectsRoot := filepath.Join(root, "projects")
	projDir := filepath.Join(projectsRoot, "-home-alice-work-demo")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := fakeCLIKey()
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-11-20T10:00:00Z","cwd":"/home/alice/work/demo","message":{"role":"user","content":"Fix cache bug. Email alice@example.com with ` + secret + `"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-20T10:01:00Z","message":{"role":"assistant","model":"claude-opus","content":[{"type":"tool_use","id":"tu1","name":"Edit","input":{"file_path":"a.go","old_string":"x"}}]}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-11-20T10:02:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":"match failed"}]}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-11-20T10:03:00Z","message":{"role":"assistant","model":"claude-opus","content":"Fixed and go test ./... passed."}}`,
	}
	if err := os.WriteFile(filepath.Join(projDir, "sess-1.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "ingest", "claude_code", "--path", projectsRoot, "--db", db)

	// 1. List packs.
	out := run(t, "label", "packs")
	for _, want := range []string{"session-triage", "user-turn-boundaries", "benchmark-candidacy", "tool-failures"} {
		if !strings.Contains(out, want) {
			t.Errorf("label packs missing %q:\n%s", want, out)
		}
	}

	// 2. Dry-run with --show-state works without API key and redacts secrets/PII.
	t.Setenv("TYPESAFE_API_KEY", "")
	dry := run(t, "label", "run", "session-triage", "--db", db, "--dry-run", "--show-state", "--min-age", "0s")
	if strings.Contains(dry, "alice@example.com") || strings.Contains(dry, "sk-proj-") {
		t.Fatalf("dry-run leaked unredacted secret/PII:\n%s", dry)
	}
	if !strings.Contains(dry, "[REDACTED:EMAIL]") {
		t.Errorf("expected [REDACTED:EMAIL] in dry-run state:\n%s", dry)
	}

	// 3. Live run --all against httptest stub server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model     string         `json:"model"`
			State     map[string]any `json:"state"`
			Questions map[string]struct {
				Type     string `json:"type"`
				Criteria any    `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rawState, _ := json.Marshal(req.State)
		if strings.Contains(string(rawState), "alice@example.com") || strings.Contains(string(rawState), "sk-proj-") {
			http.Error(w, "unredacted secret sent", http.StatusBadRequest)
			return
		}
		answers := map[string]any{}
		for qid, q := range req.Questions {
			switch q.Type {
			case "noul":
				v := 0.9
				if qid == "sensitive_or_secrets_risk" {
					v = 0.05
				}
				answers[qid] = map[string]any{"type": "noul", "noul": v}
			case "choice":
				choice := "other"
				if m, ok := q.Criteria.(map[string]any); ok {
					for k := range m {
						choice = k
						break
					}
					if _, ok := m["new_task_spec"]; ok {
						choice = "new_task_spec"
					}
				}
				answers[qid] = map[string]any{
					"type":          "choice",
					"choice":        choice,
					"confidence":    0.9,
					"probabilities": map[string]float64{choice: 0.9},
				}
			case "score":
				answers[qid] = map[string]any{
					"type":       "score",
					"score":      2.0,
					"confidence": 0.85,
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   req.Model,
			"answers": answers,
			"usage":   map[string]any{"input_tokens": 120},
		})
	}))
	defer srv.Close()

	t.Setenv("TYPESAFE_API_KEY", "test-key-never-logged")
	t.Setenv("TYPESAFE_BASE_URL", srv.URL)

	liveOut := run(t, "label", "run", "--all", "--db", db, "--min-age", "0s", "--yes")
	if !strings.Contains(liveOut, "Run saved:") {
		t.Fatalf("expected Run saved in output:\n%s", liveOut)
	}

	// Verify all 4 packs wrote labels.
	countLabels := func() int {
		s, err := store.OpenReadOnly(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		var n int
		if err := s.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM labels`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	beforeRebuild := countLabels()
	if beforeRebuild < 15 {
		t.Fatalf("expected labels from all 4 packs, got %d", beforeRebuild)
	}

	// 4. Rebuild must replay every label from chatstrata-go.labels/.
	rebuildOut := run(t, "rebuild", "--db", db)
	if !strings.Contains(rebuildOut, "Labels:") {
		t.Errorf("rebuild output missing Labels count:\n%s", rebuildOut)
	}
	if afterRebuild := countLabels(); afterRebuild != beforeRebuild {
		t.Fatalf("labels lost across rebuild: before=%d after=%d", beforeRebuild, afterRebuild)
	}

	// 5. Summary and runs commands work.
	sumOut := run(t, "label", "summary", "session-triage", "--db", db)
	if !strings.Contains(sumOut, "is_real_work_session") {
		t.Errorf("unexpected summary:\n%s", sumOut)
	}
	runsOut := run(t, "label", "runs", "--db", db)
	if !strings.Contains(runsOut, "benchmark-candidacy") {
		t.Errorf("expected benchmark-candidacy in label runs:\n%s", runsOut)
	}

	// 6. Clear removes durable run files so rebuild does not resurrect them.
	run(t, "label", "clear", "session-triage", "--db", db, "--yes")
	run(t, "rebuild", "--db", db)
	runs, err := label.ReadRuns(label.Dir(db))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Header.Pack == "session-triage" {
			t.Errorf("session-triage run survived clear: %s", r.Header.RunID)
		}
	}

	// 7. Non-interactive without --yes fails safely without calling API.
	var stdout, stderr bytes.Buffer
	code := Run(ctx, []string{"label", "run", "session-triage", "--db", db, "--min-age", "0s"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--yes") {
		t.Errorf("expected usage error requiring --yes, got code=%d stderr=%q", code, stderr.String())
	}
}
