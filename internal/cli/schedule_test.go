package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeScheduler records service-manager commands instead of running them.
func fakeScheduler(t *testing.T, goos string) (*scheduler, *[]string) {
	t.Helper()
	home := t.TempDir()
	var calls []string
	return &scheduler{
		goos: goos, home: home, configHome: filepath.Join(home, ".config"), uid: 501,
		run: func(name string, args ...string) (string, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if name == "systemctl" && len(args) > 1 && args[1] == "show" {
				return "ActiveState=active\nSubState=running\nExecMainStatus=0\n", nil
			}
			if name == "launchctl" && args[0] == "print" {
				return "\tstate = running\n\tlast exit code = (never exited)\n", nil
			}
			return "", nil
		},
	}, &calls
}

func runSched(t *testing.T, s *scheduler, f func(*env, *scheduler, []string) error, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := f(&env{ctx: context.Background(), stdout: &out, stderr: &out}, s, args); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return out.String()
}

func TestScheduleSystemd(t *testing.T) {
	s, calls := fakeScheduler(t, "linux")
	bin := "/opt/chatstrata dir/chatstrata"
	runSched(t, s, scheduleInstall, "--binary", bin, "--interval", "10", "--db", "/data/a%b.duckdb")

	unit, err := os.ReadFile(s.unitPath())
	if err != nil {
		t.Fatal(err)
	}
	want := `ExecStart="/opt/chatstrata dir/chatstrata" "daemon" "--interval" "10m0s" "--db" "/data/a%%b.duckdb"`
	if !strings.Contains(string(unit), want+"\n") || !strings.Contains(string(unit), "Restart=on-failure") {
		t.Errorf("unit:\n%s", unit)
	}
	if got := strings.Join(*calls, "; "); got != "systemctl --user daemon-reload; systemctl --user enable chatstrata-daemon.service; systemctl --user restart chatstrata-daemon.service" {
		t.Errorf("calls = %s", got)
	}

	out := runSched(t, s, scheduleStatus)
	for _, w := range []string{"Status:   active (running)", "Binary:   " + bin, "Interval: 10m0s", "Database: /data/a%b.duckdb"} {
		if !strings.Contains(out, w) {
			t.Errorf("status lacks %q:\n%s", w, out)
		}
	}

	// A Python-era timer is pointed out, since it keeps running the Python app.
	if err := writeFile(filepath.Join(s.configHome, "systemd", "user", pythonSystemdTimer), "[Timer]\n"); err != nil {
		t.Fatal(err)
	}
	if out := runSched(t, s, scheduleStatus); !strings.Contains(out, "The Python app's scheduled ingest is also installed") {
		t.Errorf("Python timer not mentioned:\n%s", out)
	}

	runSched(t, s, scheduleUninstall)
	if _, err := os.Stat(s.unitPath()); !os.IsNotExist(err) {
		t.Error("unit still installed")
	}
	if out := runSched(t, s, scheduleStatus); !strings.Contains(out, "No scheduled daemon is installed") {
		t.Errorf("status after uninstall:\n%s", out)
	}
}

func TestScheduleReplacesHandMadeUnit(t *testing.T) {
	s, _ := fakeScheduler(t, "linux")
	if err := writeFile(s.unitPath(), "[Service]\nExecStart=/usr/local/bin/chatstrata daemon\n"); err != nil {
		t.Fatal(err)
	}
	if out := runSched(t, s, scheduleInstall, "--binary", "/b/chatstrata"); !strings.Contains(out, "which wasn't installed by chatstrata") {
		t.Errorf("replacing a hand-made unit not mentioned:\n%s", out)
	}
	if out := runSched(t, s, scheduleInstall, "--binary", "/b/chatstrata"); strings.Contains(out, "wasn't installed by chatstrata") {
		t.Errorf("our own unit reported as foreign:\n%s", out)
	}
}

func TestScheduleLaunchd(t *testing.T) {
	s, calls := fakeScheduler(t, "darwin")
	bin := "/Users/me/bin/chat & strata"
	runSched(t, s, scheduleInstall, "--binary", bin, "--interval", "1h")
	plist, err := os.ReadFile(s.plistPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"<string>/Users/me/bin/chat &amp; strata</string>", "<string>1h0m0s</string>", "<key>KeepAlive</key>", "daemon.log"} {
		if !strings.Contains(string(plist), w) {
			t.Errorf("plist lacks %q:\n%s", w, plist)
		}
	}
	if got := strings.Join(*calls, "; "); !strings.Contains(got, "launchctl bootstrap gui/501 "+s.plistPath()) {
		t.Errorf("calls = %s", got)
	}
	out := runSched(t, s, scheduleStatus)
	for _, w := range []string{"Status:   running", "Binary:   " + bin, "Interval: 1h0m0s"} {
		if !strings.Contains(out, w) {
			t.Errorf("status lacks %q:\n%s", w, out)
		}
	}
	runSched(t, s, scheduleUninstall)
	if _, err := os.Stat(s.plistPath()); !os.IsNotExist(err) {
		t.Error("plist still installed")
	}
}

func TestParseInterval(t *testing.T) {
	for in, want := range map[string]string{"15": "15m0s", "15m": "15m0s", "1h": "1h0m0s", "900s": "15m0s"} {
		if d, err := parseInterval(in); err != nil || d.String() != want {
			t.Errorf("parseInterval(%q) = %v, %v", in, d, err)
		}
	}
	if _, err := parseInterval("soon"); err == nil {
		t.Error("parseInterval(soon) succeeded")
	}
}
