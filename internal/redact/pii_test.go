package redact

import (
	"os/user"
	"strings"
	"testing"
)

func piiEngine(t *testing.T, p Policy) *Engine {
	t.Helper()
	p.DisableLocalUsername = true
	e, err := NewEngine(p)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEmails(t *testing.T) {
	e := piiEngine(t, DefaultPolicy())
	for _, tc := range []struct {
		text string
		want string
	}{
		{"contact alice@example.com", "contact [REDACTED:EMAIL]"},
		{"ALICE@EXAMPLE.COM", "[REDACTED:EMAIL]"},
		{"alice@example.co.uk", "[REDACTED:EMAIL]"},
	} {
		result, err := e.Redact(tc.text)
		if err != nil {
			t.Fatal(err)
		}
		if result.Text != tc.want {
			t.Errorf("%q -> %q, want %q", tc.text, result.Text, tc.want)
		}
	}
	for _, text := range []string{
		"a@b",
		"a@b..com",
		"a@-b.com",
		"a@b.c",
		"alice@localhost",
	} {
		result, err := e.Redact(text)
		if err != nil {
			t.Fatal(err)
		}
		if result.Text != text || len(result.Findings) != 0 {
			t.Errorf("%q changed: %q %+v", text, result.Text, result.Findings)
		}
	}
}

func TestIPAddresses(t *testing.T) {
	e := piiEngine(t, DefaultPolicy())
	for _, text := range []string{
		"10.1.2.3",
		"2001:db8::1",
		"::ffff:10.1.2.3",
		"fe80::1%eth0",
	} {
		result, err := e.Redact(text)
		if err != nil {
			t.Fatal(err)
		}
		if result.Text != "[REDACTED:IP]" {
			t.Errorf("%q -> %q", text, result.Text)
		}
	}
	for _, text := range []string{
		"127.0.0.1",
		"::1",
		"0.0.0.0",
		"999.1.2.3",
		"12:30:45",
		"aa:bb:cc:dd:ee:ff",
	} {
		result, err := e.Redact(text)
		if err != nil {
			t.Fatal(err)
		}
		if result.Text != text || len(result.Findings) != 0 {
			t.Errorf("%q changed: %q %+v", text, result.Text, result.Findings)
		}
	}
}

func TestHomePathsRedactOnlyTheUserSegment(t *testing.T) {
	e := piiEngine(t, DefaultPolicy())
	for _, tc := range []struct{ text, want string }{
		{"/home/alice/work/project.go", "/home/[REDACTED:USERNAME]/work/project.go"},
		{"/Users/alice/Library", "/Users/[REDACTED:USERNAME]/Library"},
		{`C:\Users\Alice\project\main.go`, `C:\Users\[REDACTED:USERNAME]\project\main.go`},
		{`"C:\Users\Alice"`, `"C:\Users\[REDACTED:USERNAME]"`},
	} {
		result, err := e.Redact(tc.text)
		if err != nil {
			t.Fatal(err)
		}
		if result.Text != tc.want {
			t.Errorf("%q -> %q, want %q", tc.text, result.Text, tc.want)
		}
	}
	for _, text := range []string{"/Users/Shared/project", `C:\Users\Public\project`} {
		result, err := e.Redact(text)
		if err != nil {
			t.Fatal(err)
		}
		if result.Text != text {
			t.Errorf("%q changed: %q", text, result.Text)
		}
	}
}

func TestConfiguredUsernamesUseWordBoundaries(t *testing.T) {
	e := piiEngine(t, Policy{
		Profile:              ProfileBalanced,
		MaxInputBytes:        DefaultMaxInputBytes,
		Usernames:            []string{"alice"},
		DisableLocalUsername: true,
	})
	for _, text := range []string{"alice wrote this", "Alice wrote this"} {
		result, err := e.Redact(text)
		if err != nil {
			t.Fatal(err)
		}
		if result.Text != "[REDACTED:USERNAME] wrote this" {
			t.Errorf("%q -> %q", text, result.Text)
		}
	}
	for _, text := range []string{"malice", "alice@example.com"} {
		result, err := e.Redact(text)
		if err != nil {
			t.Fatal(err)
		}
		if text == "malice" && result.Text != text {
			t.Errorf("%q changed: %q", text, result.Text)
		}
		if text == "alice@example.com" && result.Text != "[REDACTED:EMAIL]" {
			t.Errorf("email should win over username: %q", result.Text)
		}
	}
}

func TestPrivateHostnames(t *testing.T) {
	e := piiEngine(t, Policy{
		Profile:              ProfileBalanced,
		MaxInputBytes:        DefaultMaxInputBytes,
		PrivateHosts:         []string{"buildbox"},
		DisableLocalUsername: true,
	})
	for _, text := range []string{"db.internal", "mac.local", "buildbox.corp", "buildbox", "db.internal:5432"} {
		result, err := e.Redact(text)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Text, "[REDACTED:HOSTNAME]") {
			t.Errorf("%q -> %q", text, result.Text)
		}
	}
	for _, text := range []string{"example.com", "db.internal.example.com", "buildbox.example.com"} {
		result, err := e.Redact(text)
		if err != nil {
			t.Fatal(err)
		}
		if result.Text != text || len(result.Findings) != 0 {
			t.Errorf("%q changed: %q %+v", text, result.Text, result.Findings)
		}
	}
}

func TestLocalUsernameIsIncludedByDefault(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skip("no current user")
	}
	name := u.Username
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	if len(name) < 3 || !isASCII(name) {
		t.Skip("current username is unusable for this test")
	}
	if _, skip := commonUsernames[strings.ToLower(name)]; skip {
		t.Skip("current username is a shared account")
	}
	engine, err := NewEngine(DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Redact("hello " + name)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello [REDACTED:USERNAME]" {
		t.Fatalf("%q -> %q", name, result.Text)
	}
}

func TestPIIPreservesURLAndPortShape(t *testing.T) {
	e := piiEngine(t, DefaultPolicy())
	text := "POST https://db.internal:8443/v1 to 10.1.2.3:8080, [2001:db8::1]:443"
	result, err := e.Redact(text)
	if err != nil {
		t.Fatal(err)
	}
	want := "POST https://[REDACTED:HOSTNAME]:8443/v1 to [REDACTED:IP]:8080, [[REDACTED:IP]]:443"
	if result.Text != want {
		t.Fatalf("redacted = %q, want %q", result.Text, want)
	}
}

func TestPrivateHostnameTrailingDot(t *testing.T) {
	e := piiEngine(t, DefaultPolicy())
	result, err := e.Redact("lookup db.internal.")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "lookup [REDACTED:HOSTNAME]." {
		t.Fatalf("redacted = %q", result.Text)
	}
}

func TestEscapedWindowsPathInJSONL(t *testing.T) {
	e := piiEngine(t, DefaultPolicy())
	text := `{"cwd":"C:\\Users\\Alice\\project"}`
	result, err := e.Redact(text)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Text, "Alice") {
		t.Fatalf("username survived: %q", result.Text)
	}
	if !strings.Contains(result.Text, `C:\\Users\\[REDACTED:USERNAME]\\project`) {
		t.Fatalf("path shape changed: %q", result.Text)
	}
}
