package cli

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/brandonbosch/chatstrata/internal/store"
)

// `schedule` installs `chatstrata daemon` as a per-user background service:
// a systemd user unit on Linux, a launchd agent on macOS. The Python app's
// `schedule` ran a one-shot ingest from a timer; the Go app has a
// long-running daemon that collects and syncs on its own interval, so the
// service manager only keeps it running.

const (
	systemdUnit   = "chatstrata-daemon.service"
	launchdLabel  = "com.chatstrata.daemon"
	installedMark = "# Installed by `chatstrata schedule install`; remove with `chatstrata schedule uninstall`."
	// The Python app's scheduled ingest, which keeps running the Python
	// version until it is removed.
	pythonSystemdTimer = "chatstrata-sync.timer"
	pythonLaunchdLabel = "com.chatstrata.sync"
)

// scheduler abstracts the OS bits so tests can run every platform's path.
type scheduler struct {
	goos string
	home string
	// configHome is $XDG_CONFIG_HOME or ~/.config.
	configHome string
	uid        int
	run        func(name string, args ...string) (string, error)
}

func defaultScheduler() (*scheduler, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home directory: %w", err)
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	return &scheduler{
		goos: runtime.GOOS, home: home, configHome: config, uid: os.Getuid(),
		run: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return string(out), err
		},
	}, nil
}

func (s *scheduler) unitPath() string {
	return filepath.Join(s.configHome, "systemd", "user", systemdUnit)
}

func (s *scheduler) plistPath() string {
	return filepath.Join(s.home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func (s *scheduler) logDir() string {
	return filepath.Join(s.home, "Library", "Logs", "chatstrata")
}

func runSchedule(e *env, args []string) error {
	if len(args) == 0 {
		return usageError{"usage: chatstrata schedule install|uninstall|status"}
	}
	s, err := defaultScheduler()
	if err != nil {
		return err
	}
	switch args[0] {
	case "install":
		return scheduleInstall(e, s, args[1:])
	case "uninstall":
		return scheduleUninstall(e, s, args[1:])
	case "status":
		return scheduleStatus(e, s, args[1:])
	}
	return usageError{fmt.Sprintf("unknown schedule command %q: use install, uninstall or status", args[0])}
}

// parseInterval accepts Go durations (5m, 1h30m) and, like the Python app,
// a bare number of minutes.
func parseInterval(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Minute, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid interval %q (use e.g. 5m, 1h or a number of minutes)", v)
	}
	return d, nil
}

func scheduleInstall(e *env, s *scheduler, args []string) error {
	fs := newFlagSet(e, "schedule install", "schedule install [--interval 5m] [--binary PATH] [--db PATH]")
	intervalFlag := fs.String("interval", "5m", "Time between collect-and-sync runs (e.g. 5m, 1h).")
	binary := fs.String("binary", "", "Path to the chatstrata binary (default: this binary).")
	db := fs.String("db", "", "Database the daemon uses (default: the usual one).")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	interval, err := parseInterval(*intervalFlag)
	if err != nil {
		return usageError{err.Error()}
	}
	if interval < 10*time.Second {
		return usageError{"--interval must be at least 10s"}
	}
	bin := *binary
	if bin == "" {
		if bin, err = os.Executable(); err != nil {
			return fmt.Errorf("locate the chatstrata binary (pass --binary): %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(bin); err == nil {
			bin = resolved
		}
	}
	argv := []string{bin, "daemon", "--interval", interval.String()}
	if *db != "" {
		path, err := store.ResolvePath(*db)
		if err != nil {
			return err
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		argv = append(argv, "--db", path)
	}

	switch s.goos {
	case "linux":
		path := s.unitPath()
		if replaced := replacingForeign(path); replaced {
			fmt.Fprintf(e.stdout, "Replacing %s, which wasn't installed by chatstrata.\n", path)
		}
		if err := writeFile(path, systemdService(argv)); err != nil {
			return err
		}
		for _, cmd := range [][]string{{"daemon-reload"}, {"enable", systemdUnit}, {"restart", systemdUnit}} {
			if out, err := s.run("systemctl", append([]string{"--user"}, cmd...)...); err != nil {
				return fmt.Errorf("systemctl --user %s: %s", strings.Join(cmd, " "), strings.TrimSpace(firstLine(out, err)))
			}
		}
		fmt.Fprintf(e.stdout, "Installed the chatstrata daemon as a systemd user service (runs every %s).\n", interval)
		fmt.Fprintf(e.stdout, "  Unit: %s\n", path)
		fmt.Fprintf(e.stdout, "  Logs: journalctl --user -u %s\n", systemdUnit)
		fmt.Fprintln(e.stdout, "\nIt runs while you're logged in. To keep it running after you log out: loginctl enable-linger")
	case "darwin":
		path := s.plistPath()
		if replaced := replacingForeign(path); replaced {
			fmt.Fprintf(e.stdout, "Replacing %s, which wasn't installed by chatstrata.\n", path)
		}
		if err := os.MkdirAll(s.logDir(), 0o755); err != nil {
			return err
		}
		if err := writeFile(path, launchdPlist(argv, s.logDir())); err != nil {
			return err
		}
		domain := fmt.Sprintf("gui/%d", s.uid)
		s.run("launchctl", "bootout", domain+"/"+launchdLabel) // not loaded yet is fine
		if out, err := s.run("launchctl", "bootstrap", domain, path); err != nil {
			return fmt.Errorf("launchctl bootstrap: %s", strings.TrimSpace(firstLine(out, err)))
		}
		fmt.Fprintf(e.stdout, "Installed the chatstrata daemon as a launchd agent (runs every %s).\n", interval)
		fmt.Fprintf(e.stdout, "  Plist: %s\n", path)
		fmt.Fprintf(e.stdout, "  Logs:  %s\n", filepath.Join(s.logDir(), "daemon.log"))
	default:
		return fmt.Errorf("schedule isn't supported on %s; run `chatstrata daemon` from your own service manager", s.goos)
	}
	fmt.Fprintln(e.stdout, "Use `chatstrata schedule status` to check it, `chatstrata schedule uninstall` to remove it.")
	warnPythonSchedule(e, s)
	return nil
}

func scheduleUninstall(e *env, s *scheduler, args []string) error {
	fs := newFlagSet(e, "schedule uninstall", "schedule uninstall")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	switch s.goos {
	case "linux":
		path := s.unitPath()
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintln(e.stdout, "No scheduled daemon is installed.")
			return nil
		}
		s.run("systemctl", "--user", "disable", "--now", systemdUnit)
		if err := os.Remove(path); err != nil {
			return err
		}
		s.run("systemctl", "--user", "daemon-reload")
	case "darwin":
		path := s.plistPath()
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintln(e.stdout, "No scheduled daemon is installed.")
			return nil
		}
		s.run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", s.uid, launchdLabel))
		if err := os.Remove(path); err != nil {
			return err
		}
	default:
		return fmt.Errorf("schedule isn't supported on %s", s.goos)
	}
	fmt.Fprintln(e.stdout, "Scheduled daemon removed.")
	return nil
}

func scheduleStatus(e *env, s *scheduler, args []string) error {
	fs := newFlagSet(e, "schedule status", "schedule status")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var path string
	switch s.goos {
	case "linux":
		path = s.unitPath()
	case "darwin":
		path = s.plistPath()
	default:
		return fmt.Errorf("schedule isn't supported on %s", s.goos)
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(e.stdout, "No scheduled daemon is installed.")
		fmt.Fprintln(e.stdout, "Run `chatstrata schedule install` to collect and sync in the background.")
		warnPythonSchedule(e, s)
		return nil
	}
	if err != nil {
		return err
	}

	state := "unknown"
	switch s.goos {
	case "linux":
		out, _ := s.run("systemctl", "--user", "show", systemdUnit, "--property=ActiveState", "--property=SubState", "--property=ExecMainStatus")
		props := map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				props[k] = v
			}
		}
		if props["ActiveState"] != "" {
			state = props["ActiveState"] + " (" + props["SubState"] + ")"
			if props["ActiveState"] != "active" && props["ExecMainStatus"] != "" && props["ExecMainStatus"] != "0" {
				state += ", last exit " + props["ExecMainStatus"]
			}
		}
	case "darwin":
		out, err := s.run("launchctl", "print", fmt.Sprintf("gui/%d/%s", s.uid, launchdLabel))
		if err != nil {
			state = "installed but not loaded"
		} else {
			state = "loaded"
			for _, line := range strings.Split(out, "\n") {
				line = strings.TrimSpace(line)
				if v, ok := strings.CutPrefix(line, "state = "); ok {
					state = v
				}
				if v, ok := strings.CutPrefix(line, "last exit code = "); ok && v != "0" && v != "(never exited)" {
					state += ", last exit " + v
				}
			}
		}
	}
	argv := installedArgv(s.goos, string(content))
	fmt.Fprintf(e.stdout, "Status:   %s\n", state)
	if len(argv) > 0 {
		fmt.Fprintf(e.stdout, "Binary:   %s\n", argv[0])
		for i, a := range argv {
			if a == "--interval" && i+1 < len(argv) {
				fmt.Fprintf(e.stdout, "Interval: %s\n", argv[i+1])
			}
			if a == "--db" && i+1 < len(argv) {
				fmt.Fprintf(e.stdout, "Database: %s\n", argv[i+1])
			}
		}
	}
	if s.goos == "linux" {
		fmt.Fprintf(e.stdout, "Unit:     %s\n", path)
		fmt.Fprintf(e.stdout, "Logs:     journalctl --user -u %s\n", systemdUnit)
	} else {
		fmt.Fprintf(e.stdout, "Plist:    %s\n", path)
		fmt.Fprintf(e.stdout, "Logs:     %s\n", filepath.Join(s.logDir(), "daemon.log"))
	}
	if exe, err := os.Executable(); err == nil && len(argv) > 0 {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != argv[0] {
			fmt.Fprintf(e.stdout, "Note:     the service runs %s, not this binary (%s); reinstall to switch.\n", argv[0], resolved)
		}
	}
	warnPythonSchedule(e, s)
	return nil
}

// warnPythonSchedule points out the Python app's scheduled ingest, which
// keeps running the Python version next to the Go daemon.
func warnPythonSchedule(e *env, s *scheduler) {
	var path, remove string
	switch s.goos {
	case "linux":
		path = filepath.Join(s.configHome, "systemd", "user", pythonSystemdTimer)
		remove = "systemctl --user disable --now " + pythonSystemdTimer
	case "darwin":
		path = filepath.Join(s.home, "Library", "LaunchAgents", pythonLaunchdLabel+".plist")
		remove = fmt.Sprintf("launchctl bootout gui/%d/%s", s.uid, pythonLaunchdLabel)
	default:
		return
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(e.stdout, "\nThe Python app's scheduled ingest is also installed (%s); it still runs the Python version.\n", path)
		fmt.Fprintf(e.stdout, "Remove it with the Python app's `chatstrata schedule uninstall`, or: %s\n", remove)
	}
}

// replacingForeign reports whether path exists and wasn't written by us,
// such as a hand-made unit with the same name.
func replacingForeign(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && !strings.Contains(string(b), installedMark) && !strings.Contains(string(b), xmlComment(installedMark))
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func firstLine(out string, err error) string {
	if s := strings.TrimSpace(out); s != "" {
		return strings.Split(s, "\n")[0]
	}
	return err.Error()
}

// systemdQuote quotes one ExecStart argument; systemd expands % and $.
func systemdQuote(arg string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	return `"` + r.Replace(arg) + `"`
}

func systemdService(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = systemdQuote(a)
	}
	return installedMark + `
[Unit]
Description=chatstrata daemon: collect and sync AI conversations
After=network-online.target

[Service]
Type=simple
ExecStart=` + strings.Join(quoted, " ") + `
Restart=on-failure
RestartSec=30
Nice=10
IOSchedulingClass=idle

[Install]
WantedBy=default.target
`
}

func xmlComment(s string) string { return "<!-- " + s + " -->" }

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func launchdPlist(argv []string, logDir string) string {
	var args strings.Builder
	for _, a := range argv {
		args.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
` + xmlComment(installedMark) + `
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
` + args.String() + `	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + xmlEscape(filepath.Join(logDir, "daemon.log")) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(filepath.Join(logDir, "daemon.err")) + `</string>
</dict>
</plist>
`
}

// installedArgv reads the command line back from an installed unit or plist.
func installedArgv(goos, content string) []string {
	var argv []string
	switch goos {
	case "linux":
		for _, line := range strings.Split(content, "\n") {
			rest, ok := strings.CutPrefix(line, "ExecStart=")
			if !ok {
				continue
			}
			for _, part := range strings.Split(rest, `" "`) {
				part = strings.Trim(part, `"`)
				part = strings.NewReplacer(`\\`, `\`, `\"`, `"`, "%%", "%", "$$", "$").Replace(part)
				argv = append(argv, part)
			}
		}
	case "darwin":
		_, after, ok := strings.Cut(content, "<key>ProgramArguments</key>")
		if !ok {
			return nil
		}
		array, _, _ := strings.Cut(after, "</array>")
		dec := xml.NewDecoder(strings.NewReader(array + "</array>"))
		var inString bool
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				inString = t.Name.Local == "string"
			case xml.EndElement:
				inString = false
			case xml.CharData:
				if inString {
					argv = append(argv, string(t))
				}
			}
		}
	}
	return argv
}
