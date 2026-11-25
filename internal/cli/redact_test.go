package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var syntheticXAIKey = fakeXAIKey()

func fakeXAIKey() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	r := rand.New(rand.NewSource(14))
	var b strings.Builder
	b.WriteString("xai-")
	for range 80 {
		b.WriteByte(alphabet[r.Intn(len(alphabet))])
	}
	return b.String()
}

func isolateRedactionConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CHATSTRATA_REDACTION_CONFIG", "")
}

func TestRedactTextCommand(t *testing.T) {
	isolateRedactionConfig(t)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"redact", "text", "use " + syntheticXAIKey + " now"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	if strings.Contains(got, syntheticXAIKey) {
		t.Fatalf("secret printed: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:XAI_API_KEY]") {
		t.Fatalf("placeholder missing: %q", got)
	}
}

func TestRedactTextJSONOmitsOriginal(t *testing.T) {
	isolateRedactionConfig(t)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"redact", "text", "--json", "use " + syntheticXAIKey}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var out redactJSON
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), syntheticXAIKey) {
		t.Fatal("JSON contains the secret")
	}
	if out.Version == "" || out.Redacted == "" || len(out.Findings) != 1 {
		t.Fatalf("result = %+v", out)
	}
	if out.Findings[0].Rule != "xai-api-key" {
		t.Fatalf("findings = %+v", out.Findings)
	}
}

func TestRedactTextRespectsProjectDenylist(t *testing.T) {
	isolateRedactionConfig(t)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"redact", "text", "--project", "work", "--exclude-project", "work", syntheticXAIKey}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "excluded") || strings.Contains(stderr.String(), syntheticXAIKey) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRedactRulesListsAIShapes(t *testing.T) {
	isolateRedactionConfig(t)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"redact", "rules"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"openai-project-key", "anthropic-api-key", "xai-api-key", "openrouter-api-key", "pii-email", "pii-ip"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("rules output missing %s:\n%s", want, stdout.String())
		}
	}
}

func TestRedactTextPII(t *testing.T) {
	isolateRedactionConfig(t)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"redact", "text", "--json", "mail alice@example.com from /home/alice/work on 10.1.2.3 at db.internal"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var out redactJSON
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[REDACTED:EMAIL]", "/home/[REDACTED:USERNAME]/work", "[REDACTED:IP]", "[REDACTED:HOSTNAME]"} {
		if !strings.Contains(out.Redacted, want) {
			t.Errorf("missing %s in %q", want, out.Redacted)
		}
	}
	if !strings.Contains(stdout.String(), `"category": "EMAIL"`) {
		t.Fatalf("category missing: %s", stdout.String())
	}
}

func TestRedactTextConfigRule(t *testing.T) {
	isolateRedactionConfig(t)
	path := filepath.Join(t.TempDir(), "redaction.toml")
	body := `
[[redaction.rules]]
id = "acme-token"
pattern = '''ACME_KEY=([A-Za-z0-9]{20,})'''
secret_group = 1
category = "ACME_KEY"
projects = ["acme"]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"redact", "text", "--config", path, "--project", "acme", "ACME_KEY=abcdefghijklmnopqrstuvwxyz012345",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if stdout.String() != "ACME_KEY=[REDACTED:ACME_KEY]\n" {
		t.Fatalf("out = %q", stdout.String())
	}
}
