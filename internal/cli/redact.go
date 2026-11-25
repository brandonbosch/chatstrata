package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brandonbosch/chatstrata/v2/internal/redact"
)

func runRedact(e *env, args []string) error {
	if len(args) == 0 {
		return usageError{msg: "redact requires a subcommand: text or rules"}
	}
	switch args[0] {
	case "text":
		return runRedactText(e, args[1:])
	case "rules":
		return runRedactRules(e, args[1:])
	case "scan":
		return runRedactScan(e, args[1:])
	case "apply":
		return runRedactApply(e, args[1:])
	case "purge":
		return runRedactPurge(e, args[1:])
	default:
		return usageError{msg: fmt.Sprintf("unknown redact subcommand %q (want text, rules, scan, apply or purge)", args[0])}
	}
}

func runRedactText(e *env, args []string) error {
	fs := newFlagSet(e, "redact text", "redact text [TEXT] [--config PATH] [--profile fast|balanced|deep] [--project NAME] [--path PATH] [--allow-project NAME[,NAME]] [--exclude-project NAME[,NAME]] [--json]")
	config := fs.String("config", "", "TOML redaction policy (default: $CHATSTRATA_REDACTION_CONFIG, then <config>/chatstrata/redaction.toml).")
	profile := fs.String("profile", "", "Override the policy profile: fast, balanced or deep.")
	project := fs.String("project", "", "Project name for the egress policy.")
	path := fs.String("path", "", "Source path for scoped user rules.")
	allow := fs.String("allow-project", "", "Comma-separated project allowlist.")
	exclude := fs.String("exclude-project", "", "Comma-separated project denylist.")
	asJSON := fs.Bool("json", false, "Print redacted text and payload-free findings as JSON.")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError{msg: "redact text requires TEXT"}
	}
	text := strings.Join(pos, " ")

	policy, err := redact.LoadPolicy(*config)
	if err != nil {
		return err
	}
	if *profile != "" {
		policy.Profile, err = redact.ParseProfile(*profile)
		if err != nil {
			return usageError{msg: err.Error()}
		}
	}
	gate, err := redact.NewGate(policy, redact.GateOptions{
		AllowProjects:   splitNames(*allow),
		ExcludeProjects: splitNames(*exclude),
	})
	if err != nil {
		return err
	}
	result, err := gate.RedactScope(redact.Scope{Project: *project, Path: *path}, text)
	if err != nil {
		return err
	}

	if !*asJSON {
		_, _ = fmt.Fprintln(e.stdout, result.Text)
		return nil
	}
	out := redactJSON{
		Redacted: result.Text,
		Version:  result.Version,
		Counts:   result.Counts,
	}
	for _, f := range result.Findings {
		out.Findings = append(out.Findings, redactFindingJSON{Rule: f.RuleID, Category: f.Category, Start: f.Start, End: f.End})
	}
	enc := json.NewEncoder(e.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

type redactJSON struct {
	Redacted string              `json:"redacted"`
	Version  string              `json:"redaction_version"`
	Counts   map[string]int      `json:"counts,omitempty"`
	Findings []redactFindingJSON `json:"findings,omitempty"`
}

type redactFindingJSON struct {
	Rule     string `json:"rule_id"`
	Category string `json:"category,omitempty"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
}

func runRedactRules(e *env, args []string) error {
	fs := newFlagSet(e, "redact rules", "redact rules [--config PATH] [--profile fast|balanced|deep]")
	config := fs.String("config", "", "TOML redaction policy (default: $CHATSTRATA_REDACTION_CONFIG, then <config>/chatstrata/redaction.toml).")
	profile := fs.String("profile", "", "Override the policy profile: fast, balanced or deep.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	policy, err := redact.LoadPolicy(*config)
	if err != nil {
		return err
	}
	if *profile != "" {
		policy.Profile, err = redact.ParseProfile(*profile)
		if err != nil {
			return usageError{msg: err.Error()}
		}
	}
	engine, err := redact.NewEngine(policy)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.stdout, "redaction_version: %s\nprofile: %s\nrules: %d\n", engine.Version(), policy.Profile, len(engine.RuleIDs()))
	for _, id := range engine.RuleIDs() {
		_, _ = fmt.Fprintf(e.stdout, "  %s\n", id)
	}
	return nil
}

func splitNames(s string) []string {
	var out []string
	for _, name := range strings.Split(s, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}
