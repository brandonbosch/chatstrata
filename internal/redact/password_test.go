package redact

import (
	"strings"
	"testing"
)

func TestPasswordAssignments(t *testing.T) {
	engine := newTestEngine(t, DefaultPolicy())
	tests := []struct {
		name string
		text string
		key  string
	}{
		{"quoted weak password", `password = "hunter2"`, "hunter2"},
		{"pg env password", "PGPASSWORD=hunter2", "hunter2"},
		{"json password", `{"db_password": "s3cr3t!"}`, "s3cr3t!"},
		{"quoted passphrase", `password = "correct horse battery staple"`, "correct horse battery staple"},
		{"unquoted shell password", "export DB_PASSWORD=P@ssw0rd!", "P@ssw0rd!"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := engine.Redact(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(result.Text, tc.key) {
				t.Fatalf("password survived: %q", result.Text)
			}
			if !strings.Contains(result.Text, "[REDACTED:PASSWORD_ASSIGNMENT]") {
				t.Fatalf("redacted = %q", result.Text)
			}
		})
	}
}

func TestPasswordPlaceholdersAndReferencesStay(t *testing.T) {
	engine := newTestEngine(t, DefaultPolicy())
	for _, text := range []string{
		`password = "changeme"`,
		`password = os.getenv("DB_PASSWORD")`,
		`password = "${DB_PASSWORD}"`,
		`password = $DB_PASSWORD`,
		`password = $(cat secret)`,
		`password = String(data.get("password"))`,
		`password: files systemd`,
		`$ pwd`,
		`pwd`,
	} {
		result, err := engine.Redact(text)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if len(result.Findings) != 0 || result.Text != text {
			t.Errorf("%q changed: %q %+v", text, result.Text, result.Findings)
		}
	}
}

func TestPasswordRuleIsListed(t *testing.T) {
	engine := newTestEngine(t, DefaultPolicy())
	for _, id := range engine.RuleIDs() {
		if id == "password-assignment" {
			return
		}
	}
	t.Fatal("password-assignment missing from rule list")
}

func TestPasswordInsideRawJSONL(t *testing.T) {
	engine := newTestEngine(t, DefaultPolicy())
	text := `{"type":"user","message":{"content":"password = \"hunter2\""}}`
	result, err := engine.Redact(text)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Text, "hunter2") {
		t.Fatalf("escaped JSON password survived: %q", result.Text)
	}
	if !strings.Contains(result.Text, `\"[REDACTED:PASSWORD_ASSIGNMENT]\"`) {
		t.Fatalf("placeholder missing: %q", result.Text)
	}
}
