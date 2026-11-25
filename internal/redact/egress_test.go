package redact

import (
	"errors"
	"strings"
	"testing"
)

func TestGateDeniesExcludedProjectBeforeRedacting(t *testing.T) {
	gate, err := NewGate(DefaultPolicy(), GateOptions{ExcludeProjects: []string{"work"}})
	if err != nil {
		t.Fatal(err)
	}
	key := xai
	_, err = gate.Redact("work", key)
	if !errors.Is(err, ErrProjectDenied) {
		t.Fatalf("err = %v", err)
	}
	out, _, err := gate.RedactFields("work", map[string]string{"prompt": key})
	if !errors.Is(err, ErrProjectDenied) || out != nil {
		t.Fatalf("fields err=%v out=%v", err, out)
	}
	if strings.Contains(err.Error(), key) {
		t.Fatal("project error contains the text")
	}
}

func TestGateAllowListRequiresKnownProject(t *testing.T) {
	gate, err := NewGate(DefaultPolicy(), GateOptions{AllowProjects: []string{"personal"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.CheckProject("personal"); err != nil {
		t.Fatalf("allowed project rejected: %v", err)
	}
	if err := gate.CheckProject("work"); !errors.Is(err, ErrProjectDenied) {
		t.Fatalf("unknown project allowed: %v", err)
	}
	if err := gate.CheckProject(""); !errors.Is(err, ErrProjectDenied) {
		t.Fatalf("empty project allowed with a list: %v", err)
	}
}

func TestGateRedactsEveryFieldAndLeavesInputAlone(t *testing.T) {
	gate, err := NewGate(DefaultPolicy(), GateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]string{
		"title":  "deploy notes",
		"prompt": "use " + xai,
	}
	out, report, err := gate.RedactFields("personal", input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out["prompt"], "[REDACTED:XAI_API_KEY]") {
		t.Fatalf("prompt = %q", out["prompt"])
	}
	if out["title"] != "deploy notes" {
		t.Fatalf("title = %q", out["title"])
	}
	if strings.Contains(input["prompt"], "[REDACTED:") {
		t.Fatal("input map was modified")
	}
	if report.Version == "" || report.Fields != 2 || report.Findings == 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.Counts["xai-api-key"] != 1 {
		t.Fatalf("counts = %+v", report.Counts)
	}
}

func TestGateFailsClosedOnOversizedField(t *testing.T) {
	gate, err := NewGate(Policy{Profile: ProfileBalanced, MaxInputBytes: 16}, GateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]string{
		"ok":   "short",
		"long": xai,
	}
	out, _, err := gate.RedactFields("personal", input)
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if out != nil {
		t.Fatalf("partial output returned: %v", out)
	}
	if strings.Contains(err.Error(), input["long"]) {
		t.Fatal("error contains the field value")
	}
}

func TestGatePassesScopeToUserRules(t *testing.T) {
	policy := Policy{
		Profile:              ProfileBalanced,
		MaxInputBytes:        DefaultMaxInputBytes,
		DisableLocalUsername: true,
		Rules: []UserRule{{
			ID:          "acme-token",
			Pattern:     `ACME_KEY=([A-Za-z0-9]{20,})`,
			SecretGroup: 1,
			Category:    "ACME_KEY",
			Projects:    []string{"acme"},
		}},
	}
	gate, err := NewGate(policy, GateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	text := "ACME_KEY=abcdefghijklmnopqrstuvwxyz012345"
	for _, tc := range []struct {
		scope Scope
		want  string
	}{
		{Scope{Project: "acme"}, "ACME_KEY=[REDACTED:ACME_KEY]"},
		{Scope{Project: "other"}, text},
		{Scope{}, text},
	} {
		out, _, err := gate.RedactFieldsScope(tc.scope, map[string]string{"prompt": text})
		if err != nil {
			t.Fatal(err)
		}
		if out["prompt"] != tc.want {
			t.Errorf("scope %+v -> %q, want %q", tc.scope, out["prompt"], tc.want)
		}
	}
}
