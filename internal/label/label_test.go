package label

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/model"
	"github.com/brandonbosch/chatstrata/v2/internal/redact"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

type stubSource struct{ name string }

func (s stubSource) Name() string        { return s.name }
func (s stubSource) DisplayName() string { return s.name }
func (s stubSource) Version() string     { return "1" }
func (s stubSource) AppendOnly() bool    { return false }
func (s stubSource) Discover(string) ([]model.Handle, error) {
	return nil, nil
}
func (s stubSource) Parse(model.Handle, []byte) (*model.Conversation, error) {
	return nil, nil
}

func fakeOpenAIKey() string {
	const alpha = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	r := rand.New(rand.NewSource(7))
	var b strings.Builder
	b.WriteString("sk-proj-")
	for range 100 {
		b.WriteByte(alpha[r.Intn(len(alpha))])
	}
	return b.String()
}

func openTestArchive(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "chatstrata-go.duckdb")
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.EnsureSource(ctx, stubSource{name: "claude_code"}); err != nil {
		t.Fatal(err)
	}
	t1 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Minute)
	t3 := t1.Add(5 * time.Minute)
	t4 := t1.Add(8 * time.Minute)

	title := "Fix race condition in cache"
	proj := "chatstrata"
	rawPath := "/home/alice/work/chatstrata/session.jsonl"
	conv := &model.Conversation{
		SourceNativeID: "conv-1",
		Title:          &title,
		Project:        &proj,
		RawPath:        rawPath,
		StartedAt:      &t1,
		EndedAt:        &t4,
		Messages: []model.Message{
			{
				SourceNativeID: strPtr("m1"),
				Role:           model.RoleUser,
				CreatedAt:      &t1,
				Blocks: []model.Block{
					{Type: model.BlockText, Text: strPtr("Fix the cache lock bug. Contact alice@example.com with " + fakeOpenAIKey())},
				},
			},
			{
				SourceNativeID: strPtr("m2"),
				Role:           model.RoleAssistant,
				Model:          strPtr("claude-opus"),
				CreatedAt:      &t2,
				Blocks: []model.Block{
					{Type: model.BlockText, Text: strPtr("Checking the test suite.")},
					{Type: model.BlockToolUse, ToolName: strPtr("Edit"), ToolUseID: strPtr("tu-1"), Payload: map[string]any{"input": map[string]any{"file_path": "cache.go", "old_string": "old"}}},
				},
			},
			{
				SourceNativeID: strPtr("m3"),
				Role:           model.RoleUser,
				CreatedAt:      &t3,
				Blocks: []model.Block{
					{Type: model.BlockToolResult, ToolUseID: strPtr("tu-1"), Text: strPtr("old_string not found in file"), Payload: map[string]any{"is_error": true}},
					{Type: model.BlockText, Text: strPtr("No, you edited the wrong branch, re-read cache.go and run go test ./...")},
				},
			},
			{
				SourceNativeID: strPtr("m4"),
				Role:           model.RoleAssistant,
				Model:          strPtr("claude-opus"),
				CreatedAt:      &t4,
				Blocks: []model.Block{
					{Type: model.BlockToolUse, ToolName: strPtr("Bash"), ToolUseID: strPtr("tu-2"), Payload: map[string]any{"command": "go test ./... && git commit -m fix"}},
					{Type: model.BlockToolResult, ToolUseID: strPtr("tu-2"), Text: strPtr("ok\tchatstrata/cache\t0.1s"), Payload: map[string]any{"is_error": false}},
					{Type: model.BlockText, Text: strPtr("Fixed the lock and all tests pass.")},
				},
			},
		},
	}
	if _, err := s.Ingest(ctx, "claude_code", conv, nil); err != nil {
		t.Fatal(err)
	}

	// Add a second conversation in a denied project to verify project exclusion.
	deniedProj := "private-secret"
	deniedTitle := "Secret work"
	conv2 := &model.Conversation{
		SourceNativeID: "conv-2",
		Title:          &deniedTitle,
		Project:        &deniedProj,
		StartedAt:      &t1,
		EndedAt:        &t2,
		Messages: []model.Message{
			{SourceNativeID: strPtr("d1"), Role: model.RoleUser, CreatedAt: &t1, Blocks: []model.Block{{Type: model.BlockText, Text: strPtr("Secret prompt")}}},
			{SourceNativeID: strPtr("d2"), Role: model.RoleAssistant, CreatedAt: &t2, Blocks: []model.Block{{Type: model.BlockText, Text: strPtr("Secret reply")}}},
		},
	}
	if _, err := s.Ingest(ctx, "claude_code", conv2, nil); err != nil {
		t.Fatal(err)
	}

	var convID string
	if err := s.Conn().QueryRowContext(ctx, `SELECT id FROM conversations WHERE source_native_id = 'conv-1'`).Scan(&convID); err != nil {
		t.Fatal(err)
	}
	return s, dbPath, convID
}

func strPtr(s string) *string { return &s }
func f64Ptr(v float64) *float64 {
	return &v
}

func TestLoadBuiltinPacksAndTopologicalOrder(t *testing.T) {
	packs, err := LoadPacks("")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"session-triage", "user-turn-boundaries", "benchmark-candidacy", "tool-failures"} {
		p, ok := packs[want]
		if !ok {
			t.Fatalf("missing built-in pack %q", want)
		}
		if len(p.Version()) != 12 {
			t.Errorf("pack %s version %q want 12 hex chars", want, p.Version())
		}
	}

	ordered, err := TopologicalOrder(packs)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, p := range ordered {
		pos[p.Name] = i
	}
	if pos["session-triage"] >= pos["user-turn-boundaries"] {
		t.Errorf("session-triage must come before user-turn-boundaries: %v", pos)
	}
	if pos["user-turn-boundaries"] >= pos["benchmark-candidacy"] {
		t.Errorf("user-turn-boundaries must come before benchmark-candidacy: %v", pos)
	}

	// Custom user pack overrides built-in and rejects unknown keys.
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(bad, []byte(`name = "x"`+"\n"+`bogus_key = 1`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPacks(dir); err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Errorf("expected unknown keys error, got %v", err)
	}
}

type fakeBackend struct {
	fn func(state map[string]any, questions map[string]QuestionSpec) (AskResult, error)
}

func (f fakeBackend) Name() string { return "fake" }
func (f fakeBackend) Ask(_ context.Context, state map[string]any, questions map[string]QuestionSpec) (AskResult, error) {
	return f.fn(state, questions)
}

func TestEndToEndThreeStagePipelineRedactionAndReplay(t *testing.T) {
	ctx := context.Background()
	s, dbPath, convID := openTestArchive(t)
	labelsDir := Dir(dbPath)

	packs, err := LoadPacks("")
	if err != nil {
		t.Fatal(err)
	}
	gate, err := redact.NewGate(redact.DefaultPolicy(), redact.GateOptions{
		ExcludeProjects: []string{"private-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Stage 1: session-triage.
	triage := packs["session-triage"]
	sel, err := SelectTargets(ctx, s, triage, gate, TargetFilters{
		MinAge: DefaultMinAge,
		Now:    time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if sel.ExcludedByProject != 1 {
		t.Errorf("ExcludedByProject = %d, want 1", sel.ExcludedByProject)
	}
	if len(sel.Targets) != 1 || sel.Targets[0].ID != convID {
		t.Fatalf("selected targets = %+v, want [%s]", sel.Targets, convID)
	}
	if sel.RedactionFindings < 2 {
		t.Errorf("expected at least 2 redaction findings (email + API key), got %d", sel.RedactionFindings)
	}
	firstTurns, _ := sel.Targets[0].State["first_user_turns"].(string)
	if strings.Contains(firstTurns, "alice@example.com") || strings.Contains(firstTurns, "sk-proj-") {
		t.Fatalf("unredacted secret/PII leaked into state: %q", firstTurns)
	}
	if !strings.Contains(firstTurns, "[REDACTED:EMAIL]") {
		t.Errorf("expected [REDACTED:EMAIL] in state: %q", firstTurns)
	}

	// Active conversation younger than MinAge must be skipped.
	tooFresh, err := SelectTargets(ctx, s, triage, gate, TargetFilters{
		MinAge: DefaultMinAge,
		Now:    time.Date(2026, 11, 20, 10, 15, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tooFresh.Targets) != 0 {
		t.Errorf("expected 0 targets within settle window, got %d", len(tooFresh.Targets))
	}

	// Dependency check blocks stage 2 before stage 1 runs.
	if err := CheckDependencies(ctx, s, packs["benchmark-candidacy"]); err == nil {
		t.Fatal("expected dependency error before session-triage runs")
	}

	// Run Stage 1.
	triageStats, err := RunPack(ctx, s, triage, fakeBackend{fn: func(_ map[string]any, _ map[string]QuestionSpec) (AskResult, error) {
		return AskResult{
			Model:       "jev-1.14.0",
			InputTokens: 500,
			Answers: map[string]Answer{
				"is_real_work_session":      {Type: "noul", Noul: f64Ptr(0.95)},
				"primary_activity":          {Type: "choice", Choice: strPtr("bug_fix"), Confidence: f64Ptr(0.9), Probabilities: map[string]float64{"bug_fix": 0.9}},
				"primary_domain":            {Type: "choice", Choice: strPtr("go"), Confidence: f64Ptr(0.92), Probabilities: map[string]float64{"go": 0.92}},
				"ended_successfully":        {Type: "score", Score: f64Ptr(2.8), Confidence: f64Ptr(0.8)},
				"scope_complexity":          {Type: "score", Score: f64Ptr(2.0), Confidence: f64Ptr(0.85)},
				"verifiable_outcome":        {Type: "noul", Noul: f64Ptr(0.9)},
				"multi_task_session":        {Type: "noul", Noul: f64Ptr(0.1)},
				"sensitive_or_secrets_risk": {Type: "noul", Noul: f64Ptr(0.05)},
			},
		}, nil
	}}, sel.Targets, RunOptions{LabelsDir: labelsDir, RedactionVersion: sel.RedactionVersion})
	if err != nil {
		t.Fatal(err)
	}
	if triageStats.Labelled != 1 || triageStats.RunPath == "" {
		t.Fatalf("unexpected triage stats: %+v", triageStats)
	}

	// Incremental skip: re-selecting with same fingerprint returns 0 targets.
	selAgain, err := SelectTargets(ctx, s, triage, gate, TargetFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(selAgain.Targets) != 0 {
		t.Errorf("expected 0 targets on re-select, got %d", len(selAgain.Targets))
	}

	// Fingerprint change (conversation grew): target IS re-selected.
	if _, err := s.Conn().ExecContext(ctx, `UPDATE conversations SET content_hash = 'new-hash-after-growth' WHERE id = ?`, convID); err != nil {
		t.Fatal(err)
	}
	selGrown, err := SelectTargets(ctx, s, triage, gate, TargetFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(selGrown.Targets) != 1 {
		t.Errorf("expected grown conversation to be re-selected, got %d", len(selGrown.Targets))
	}

	// Stage 1.5: user-turn-boundaries (runs on screened conversation's user messages).
	turns := packs["user-turn-boundaries"]
	turnSel, err := SelectTargets(ctx, s, turns, gate, TargetFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(turnSel.Targets) != 2 {
		t.Fatalf("expected 2 user turns, got %d", len(turnSel.Targets))
	}
	_, err = RunPack(ctx, s, turns, fakeBackend{fn: func(_ map[string]any, _ map[string]QuestionSpec) (AskResult, error) {
		return AskResult{
			Model:       "jev-1.14.0",
			InputTokens: 200,
			Answers: map[string]Answer{
				"is_human_authored":          {Type: "noul", Noul: f64Ptr(0.96)},
				"turn_role":                  {Type: "choice", Choice: strPtr("new_task_spec"), Confidence: f64Ptr(0.88)},
				"spec_self_contained":        {Type: "score", Score: f64Ptr(2.5), Confidence: f64Ptr(0.8)},
				"states_acceptance_criteria": {Type: "noul", Noul: f64Ptr(0.85)},
				"correction_severity":        {Type: "score", Score: f64Ptr(2.0), Confidence: f64Ptr(0.8)},
				"frustration":                {Type: "score", Score: f64Ptr(0.5), Confidence: f64Ptr(0.8)},
			},
		}, nil
	}}, turnSel.Targets, RunOptions{LabelsDir: labelsDir, RedactionVersion: turnSel.RedactionVersion})
	if err != nil {
		t.Fatal(err)
	}

	// Stage 2: benchmark-candidacy (screened by session-triage + user-turn-boundaries).
	bench := packs["benchmark-candidacy"]
	benchSel, err := SelectTargets(ctx, s, bench, gate, TargetFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(benchSel.Targets) != 1 || benchSel.Targets[0].ID != convID {
		t.Fatalf("expected 1 benchmark candidate %s, got %+v", convID, benchSel.Targets)
	}

	// Tool failures pack SQL & state unwrap.
	tf := packs["tool-failures"]
	tfSel, err := SelectTargets(ctx, s, tf, gate, TargetFilters{Tool: "Edit"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tfSel.Targets) != 1 {
		t.Fatalf("expected 1 Edit tool call, got %d", len(tfSel.Targets))
	}
	if argsStr, _ := tfSel.Targets[0].State["arguments"].(string); !strings.Contains(argsStr, "cache.go") || strings.Contains(argsStr, `"input"`) {
		t.Errorf("expected unwrapped arguments JSON, got %q", argsStr)
	}

	// Durability across rebuild: wipe labels & label_runs and replay from sidecar.
	if _, err := s.Conn().ExecContext(ctx, `DELETE FROM labels; DELETE FROM label_runs`); err != nil {
		t.Fatal(err)
	}
	replayed, err := Replay(ctx, s, labelsDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Runs != 2 || replayed.Applied == 0 {
		t.Fatalf("unexpected replay stats: %+v", replayed)
	}

	// Summarize and ListRuns work on replayed rows.
	summary, err := Summarize(ctx, s, triage, "project", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary) == 0 {
		t.Error("expected non-empty summary")
	}
	runs, err := ListRuns(ctx, s, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].RedactionVersion != gate.Version() {
		t.Errorf("unexpected ListRuns result: %+v", runs)
	}

	// Clear deletes both sidecar run and DuckDB rows so replay won't resurrect it.
	deleted, err := Clear(ctx, s, labelsDir, ClearFilter{Pack: "session-triage"})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted runs = %d, want 1", deleted)
	}
	var remaining int
	if err := s.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM labels WHERE pack = 'session-triage'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("expected 0 session-triage labels after clear, got %d", remaining)
	}
}

func TestTypeSafeClientRetryAndAuthFailure(t *testing.T) {
	ctx := context.Background()
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") == "Bearer bad-secret-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"invalid api key"}`))
			return
		}
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"slow down"}`))
			return
		}
		var req systemOneRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Model != DefaultModel {
			http.Error(w, "wrong model", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.14.0","answers":{"q":{"type":"noul","noul":0.82}},"usage":{"input_tokens":77}}`))
	}))
	defer srv.Close()

	// Retry on 429 succeeds.
	client, err := NewTypeSafeClient(ClientOptions{
		APIKey:    "test-key",
		BaseURL:   srv.URL,
		BaseDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Ask(ctx, map[string]any{"x": "hello"}, map[string]QuestionSpec{
		"q": {Type: "noul", Instructions: "Is this a greeting?"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "jev-1.14.0" || res.InputTokens != 77 || res.Answers["q"].Noul == nil || *res.Answers["q"].Noul != 0.82 {
		t.Fatalf("unexpected AskResult: %+v", res)
	}

	// 401 returns FatalError and never includes the API key.
	badClient, err := NewTypeSafeClient(ClientOptions{
		APIKey:    "bad-secret-key",
		BaseURL:   srv.URL,
		BaseDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = badClient.Ask(ctx, map[string]any{"x": "hello"}, map[string]QuestionSpec{
		"q": {Type: "noul", Instructions: "test"},
	})
	var fatal *FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("expected FatalError on 401, got %T %v", err, err)
	}
	if strings.Contains(err.Error(), "bad-secret-key") {
		t.Fatalf("API key leaked into error message: %v", err)
	}
}

func TestRunPackBudgetCapRefusesBeforeSending(t *testing.T) {
	ctx := context.Background()
	s, _, convID := openTestArchive(t)
	packs, err := LoadPacks("")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = RunPack(ctx, s, packs["session-triage"], fakeBackend{fn: func(_ map[string]any, _ map[string]QuestionSpec) (AskResult, error) {
		called = true
		return AskResult{}, nil
	}}, []Target{{
		ID:    convID,
		State: map[string]any{"first_user_turns": strings.Repeat("hello world ", 5000)},
	}}, RunOptions{MaxCostUSD: 0.00001})
	if err == nil || !strings.Contains(err.Error(), "exceeds --max-cost-usd") {
		t.Fatalf("expected budget cap error, got %v", err)
	}
	if called {
		t.Fatal("backend was called despite exceeding --max-cost-usd")
	}
}
