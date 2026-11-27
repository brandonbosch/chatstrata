package redact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePolicyFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "redaction.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUserRuleWithGroupCategoryAndProjectScope(t *testing.T) {
	path := writePolicyFile(t, `
[redaction]
profile = "balanced"

[[redaction.rules]]
id = "acme-token"
pattern = '''ACME_KEY=([A-Za-z0-9]{20,})'''
secret_group = 1
category = "ACME_KEY"
priority = 90
entropy = 2.0
allowlist = ["EXAMPLE"]
projects = ["acme"]
`)
	policy, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(policy)
	if err != nil {
		t.Fatal(err)
	}
	key := "abcdefghijklmnopqrstuvwxyz012345"
	result, err := engine.RedactScope(Scope{Project: "acme"}, "ACME_KEY="+key)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "ACME_KEY=[REDACTED:ACME_KEY]" {
		t.Fatalf("redacted = %q", result.Text)
	}
	if strings.Contains(result.Text, key) {
		t.Fatal("rule value survived")
	}
	if result.Counts["acme-token"] != 1 {
		t.Fatalf("counts = %+v", result.Counts)
	}

	for _, scope := range []Scope{{}, {Project: "other"}} {
		result, err := engine.RedactScope(scope, "ACME_KEY="+key)
		if err != nil {
			t.Fatal(err)
		}
		if result.Text != "ACME_KEY="+key || len(result.Findings) != 0 {
			t.Errorf("scope %+v applied: %q %+v", scope, result.Text, result.Findings)
		}
	}

	result, err = engine.RedactScope(Scope{Project: "acme"}, "ACME_KEY=EXAMPLE000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 0 {
		t.Errorf("allowlisted value redacted: %+v", result.Findings)
	}
}

func TestDictionaryCaseAndWordBoundaries(t *testing.T) {
	path := writePolicyFile(t, `
[[redaction.dictionaries]]
id = "people"
terms = ["Alice Example", "Acme Corp"]
category = "PERSON"
`)
	policy, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(policy)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Redact("Alice Example works at acme corp")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "[REDACTED:PERSON] works at [REDACTED:PERSON]" {
		t.Fatalf("redacted = %q", result.Text)
	}
	result, err = engine.Redact("malice example")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "malice example" {
		t.Fatalf("substring matched: %q", result.Text)
	}
}

func TestUserRulePathScope(t *testing.T) {
	path := writePolicyFile(t, `
[[redaction.rules]]
id = "path-rule"
pattern = '''SECRET=[a-z0-9]{8,}'''
paths = ["/work/"]
`)
	policy, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(policy)
	if err != nil {
		t.Fatal(err)
	}
	text := "SECRET=abcd1234"
	result, err := engine.RedactScope(Scope{Path: "/work/session.jsonl"}, text)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "[REDACTED:PATH_RULE]" {
		t.Fatalf("path rule = %q", result.Text)
	}
	result, err = engine.RedactScope(Scope{Path: "/home/session.jsonl"}, text)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != text {
		t.Fatalf("path rule applied outside scope: %q", result.Text)
	}
}

func TestPolicyConfigErrors(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{
			name: "unknown key",
			body: "[redaction]\ntypo = true\n",
			want: "unknown key",
		},
		{
			name: "duplicate rule id",
			body: "[[redaction.rules]]\nid='dup'\npattern='a+'\n\n[[redaction.rules]]\nid='dup'\npattern='b+'\n",
			want: "already used",
		},
		{
			name: "invalid regex",
			body: "[[redaction.rules]]\nid='bad'\npattern='('\n",
			want: "error parsing regexp",
		},
		{
			name: "secret group out of range",
			body: "[[redaction.rules]]\nid='bad'\npattern='a+'\nsecret_group=1\n",
			want: "secret_group",
		},
		{
			name: "invalid category",
			body: "[[redaction.rules]]\nid='bad'\npattern='a+'\ncategory='bad category'\n",
			want: "category",
		},
		{
			name: "reserved id",
			body: "[[redaction.rules]]\nid='pii-email'\npattern='a+'\n",
			want: "already used",
		},
		{
			name: "empty term",
			body: "[[redaction.dictionaries]]\nid='bad'\nterms=['']\n",
			want: "2..200",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writePolicyFile(t, tc.body)
			policy, err := LoadPolicy(path)
			if err == nil {
				_, err = NewEngine(policy)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadPolicyMissingExplicitFile(t *testing.T) {
	if _, err := LoadPolicy(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("missing explicit policy accepted")
	}
}

func TestLoadPolicyDefaultFileAndMissingDefault(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv(ConfigEnv, "")
	policy, err := LoadPolicy("")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Profile != ProfileBalanced || policy.MaxInputBytes != DefaultMaxInputBytes {
		t.Fatalf("missing default = %+v", policy)
	}

	path := filepath.Join(configHome, "chatstrata", "redaction.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[redaction]\nprofile='fast'\nusernames=['alice']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err = LoadPolicy("")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Profile != ProfileFast || len(policy.Usernames) != 1 || policy.Usernames[0] != "alice" {
		t.Fatalf("default file = %+v", policy)
	}
}

func TestPolicyChangesVersion(t *testing.T) {
	base, err := NewEngine(DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	path := writePolicyFile(t, "[[redaction.rules]]\nid='extra'\npattern='extra-[a-z0-9]{8}'\n")
	policy, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	extended, err := NewEngine(policy)
	if err != nil {
		t.Fatal(err)
	}
	if base.Version() == extended.Version() {
		t.Fatal("policy change did not change redaction_version")
	}
}

func TestPolicyVersionIgnoresCollect(t *testing.T) {
	base := DefaultPolicy()
	base.DisableLocalUsername = true
	collect := base
	collect.Collect = true
	a, err := NewEngine(base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewEngine(collect)
	if err != nil {
		t.Fatal(err)
	}
	if a.Version() != b.Version() {
		t.Fatalf("collect changed the version: %s != %s", a.Version(), b.Version())
	}
}
