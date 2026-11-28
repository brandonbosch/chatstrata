package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/label"
	"github.com/brandonbosch/chatstrata/v2/internal/redact"
	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

type labelRunFlags struct {
	db             string
	packsDir       string
	config         string
	profile        string
	source         string
	project        string
	excludeProject string
	tool           string
	since          string
	until          string
	minAge         string
	limit          int
	force          bool
	all            bool
	model          string
	concurrency    int
	maxCostUSD     float64
	dryRun         bool
	showState      bool
	yes            bool
}

func runLabelRun(e *env, args []string) error {
	var f labelRunFlags
	fs := newFlagSet(e, "label run", "label run [PACK | --all] [--dry-run] [--show-state] [--yes] [filters...]")
	fs.StringVar(&f.db, "db", "", "Override the database path.")
	fs.StringVar(&f.packsDir, "packs-dir", "", "Additional TOML packs directory.")
	fs.StringVar(&f.config, "config", "", "TOML redaction policy (default: $CHATSTRATA_REDACTION_CONFIG, then <config>/chatstrata/redaction.toml).")
	fs.StringVar(&f.profile, "profile", "", "Override the redaction policy profile: fast, balanced, or deep.")
	fs.StringVar(&f.source, "source", "", "Only label targets from this source (e.g. claude_code).")
	fs.StringVar(&f.project, "project", "", "Comma-separated project allowlist (exact names).")
	fs.StringVar(&f.excludeProject, "exclude-project", "", "Comma-separated project denylist (exact names).")
	fs.StringVar(&f.tool, "tool", "", "Only label calls to this tool (tool_call packs).")
	fs.StringVar(&f.since, "since", "", "Only label items on or after this date (YYYY-MM-DD or RFC3339).")
	fs.StringVar(&f.until, "until", "", "Only label items before this date (YYYY-MM-DD or RFC3339).")
	fs.StringVar(&f.minAge, "min-age", "", "Settle window for conversation packs (default 30m; 0 disables).")
	fs.IntVar(&f.limit, "limit", 0, "Label at most N targets per pack.")
	fs.BoolVar(&f.force, "force", false, "Re-label targets even if already current.")
	fs.BoolVar(&f.all, "all", false, "Run all packs in dependency order.")
	fs.StringVar(&f.model, "model", label.DefaultModel, "Pinned TypeSafe Jev model version.")
	fs.IntVar(&f.concurrency, "concurrency", 8, "Parallel TypeSafe API requests.")
	fs.Float64Var(&f.maxCostUSD, "max-cost-usd", 0, "Abort if estimated or actual cost exceeds this USD cap (0 = unlimited).")
	fs.BoolVar(&f.dryRun, "dry-run", false, "Preview target count, redaction report, and estimated cost without calling TypeSafe.")
	fs.BoolVar(&f.showState, "show-state", false, "With --dry-run, print the first redacted state JSON.")
	fs.BoolVar(&f.yes, "yes", false, "Skip the confirmation prompt.")
	fs.BoolVar(&f.yes, "y", false, "Skip the confirmation prompt.")

	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if f.all && len(pos) > 0 {
		return usageError{msg: "pass either a PACK name or --all, not both"}
	}
	if !f.all && len(pos) != 1 {
		return usageError{msg: "label run requires a PACK name or --all"}
	}

	selectedPacks, err := pickPacks(f.packsDir, pos, f.all)
	if err != nil {
		return err
	}
	gate, err := buildLabelGate(f.config, f.profile, f.project, f.excludeProject)
	if err != nil {
		return err
	}
	baseFilters, err := parseLabelFilters(f)
	if err != nil {
		return err
	}

	if f.dryRun {
		s, err := openForReading(e, f.db)
		if s == nil || err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		return runLabelDry(e, s, selectedPacks, gate, baseFilters, f)
	}

	a, err := openArchive(e, f.db)
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()

	client, err := label.NewTypeSafeClient(label.ClientOptions{Model: f.model})
	if err != nil {
		return err
	}

	confirmed := f.yes
	for _, pack := range selectedPacks {
		filters := filtersForPack(baseFilters, pack, f.minAge)
		sel, err := label.SelectTargets(e.ctx, a.store, pack, gate, filters)
		if err != nil {
			return err
		}
		printLabelPlan(e, pack, sel, f.model)
		if len(sel.Targets) == 0 {
			continue
		}
		if !confirmed {
			if err := confirmLabelSend(e, pack.Name, len(sel.Targets), f.model); err != nil {
				return err
			}
			confirmed = true
		}
		stats, err := label.RunPack(e.ctx, a.store, pack, client, sel.Targets, label.RunOptions{
			LabelsDir:        label.Dir(a.store.Path),
			RedactionVersion: sel.RedactionVersion,
			Concurrency:      f.concurrency,
			MaxCostUSD:       f.maxCostUSD,
		})
		if stats.Labelled > 0 || stats.Failures > 0 {
			_, _ = fmt.Fprintf(e.stdout, "Labelled %d/%d items with %s (%d input tokens, %d failures).\n",
				stats.Labelled, len(sel.Targets), nonEmpty(stats.Model, f.model), stats.InputTokens, stats.Failures)
			if stats.RunPath != "" {
				_, _ = fmt.Fprintf(e.stdout, "Run saved: %s\n", stats.RunPath)
			}
			if stats.BudgetStopped {
				_, _ = fmt.Fprintf(e.stdout, "Stopped early after reaching --max-cost-usd $%.4f.\n", f.maxCostUSD)
			}
			for _, msg := range stats.Errors {
				_, _ = fmt.Fprintf(e.stderr, "  ! %s\n", msg)
			}
		}
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(e.stdout, "Summarize with: chatstrata label summary %s\n", pack.Name)
	}
	return nil
}

func runLabelDry(e *env, s *store.Store, packs []*label.Pack, gate *redact.Gate, base label.TargetFilters, f labelRunFlags) error {
	for _, pack := range packs {
		filters := filtersForPack(base, pack, f.minAge)
		sel, err := label.SelectTargets(e.ctx, s, pack, gate, filters)
		if err != nil {
			if f.all && strings.Contains(err.Error(), "depends on") {
				_, _ = fmt.Fprintf(e.stdout, "%s: skipped in --dry-run (%s)\n", pack.Name, err)
				continue
			}
			return err
		}
		printLabelPlan(e, pack, sel, f.model)
		if f.showState && len(sel.Targets) > 0 {
			enc := json.NewEncoder(e.stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(sel.Targets[0].State); err != nil {
				return err
			}
		}
	}
	return nil
}

func printLabelPlan(e *env, pack *label.Pack, sel *label.Selection, model string) {
	if len(sel.Targets) == 0 {
		_, _ = fmt.Fprintf(e.stdout, "%s: 0 targets to label (already current or no matches; excluded by project: %d).\n",
			pack.Name, sel.ExcludedByProject)
		return
	}
	tokens := label.EstimateTokens(pack, sel.Targets)
	cost := label.EstimateCostUSD(tokens, label.DefaultPricePerMTok)
	costText := fmt.Sprintf("~$%.2f", cost)
	if cost < 0.01 {
		costText = "<$0.01"
	}
	_, _ = fmt.Fprintf(e.stdout, "%s (%s, version %s): %d targets, ~%d input tokens, %s at $%.3f/Mtok (model %s)\n",
		pack.Name, pack.TargetKind, pack.Version(), len(sel.Targets), tokens, costText, label.DefaultPricePerMTok, model)
	_, _ = fmt.Fprintf(e.stdout, "  redaction_version: %s  findings scrubbed: %d  excluded by project: %d\n",
		sel.RedactionVersion, sel.RedactionFindings, sel.ExcludedByProject)
	if len(sel.RedactionCounts) > 0 {
		rules := make([]string, 0, len(sel.RedactionCounts))
		for r := range sel.RedactionCounts {
			rules = append(rules, r)
		}
		sort.Strings(rules)
		parts := make([]string, len(rules))
		for i, r := range rules {
			parts[i] = fmt.Sprintf("%s=%d", r, sel.RedactionCounts[r])
		}
		_, _ = fmt.Fprintf(e.stdout, "  redaction rules: %s\n", strings.Join(parts, ", "))
	}
}

func confirmLabelSend(e *env, packName string, count int, model string) error {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return usageError{msg: fmt.Sprintf("refusing to send %d items for %s non-interactively without --yes (or use --dry-run)", count, packName)}
	}
	_, _ = fmt.Fprintf(e.stdout, "Send %d redacted items for %s to TypeSafe (%s)? [y/N] ", count, packName, model)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if errors.Is(err, io.EOF) && line == "" {
		return usageError{msg: fmt.Sprintf("refusing to send %d items for %s non-interactively without --yes (or use --dry-run)", count, packName)}
	}
	if err != nil && line == "" {
		return errors.New("aborted")
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	if ans != "y" && ans != "yes" {
		return errors.New("aborted")
	}
	return nil
}

func pickPacks(packsDir string, pos []string, all bool) ([]*label.Pack, error) {
	if !all {
		p, err := resolvePack(packsDir, pos[0])
		if err != nil {
			return nil, err
		}
		return []*label.Pack{p}, nil
	}
	packs, err := label.LoadPacks(packsDir)
	if err != nil {
		return nil, err
	}
	return label.TopologicalOrder(packs)
}

func buildLabelGate(config, profile, allow, exclude string) (*redact.Gate, error) {
	policy, err := redact.LoadPolicy(config)
	if err != nil {
		return nil, err
	}
	if profile != "" {
		policy.Profile, err = redact.ParseProfile(profile)
		if err != nil {
			return nil, usageError{msg: err.Error()}
		}
	}
	return redact.NewGate(policy, redact.GateOptions{
		AllowProjects:   splitNames(allow),
		ExcludeProjects: splitNames(exclude),
	})
}

func parseLabelFilters(f labelRunFlags) (label.TargetFilters, error) {
	out := label.TargetFilters{
		Source:          f.source,
		Projects:        splitNames(f.project),
		ExcludeProjects: splitNames(f.excludeProject),
		Tool:            f.tool,
		Limit:           f.limit,
		Force:           f.force,
	}
	var err error
	if f.since != "" {
		out.Since, err = parseDateFlag("--since", f.since)
		if err != nil {
			return out, err
		}
	}
	if f.until != "" {
		out.Until, err = parseDateFlag("--until", f.until)
		if err != nil {
			return out, err
		}
	}
	if f.minAge != "" {
		d, err := time.ParseDuration(f.minAge)
		if err != nil || d < 0 {
			return out, usageError{msg: fmt.Sprintf("invalid --min-age %q (use a Go duration like 30m or 0s)", f.minAge)}
		}
		out.MinAge = d
	}
	return out, nil
}

func filtersForPack(base label.TargetFilters, pack *label.Pack, rawMinAge string) label.TargetFilters {
	out := base
	if rawMinAge == "" && pack.TargetKind == label.TargetConversation {
		out.MinAge = label.DefaultMinAge
	}
	if pack.TargetKind != label.TargetToolCall {
		out.Tool = ""
	}
	return out
}

func parseDateFlag(flagName, raw string) (*time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		u := t.UTC()
		return &u, nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		u := t.UTC()
		return &u, nil
	}
	return nil, usageError{msg: fmt.Sprintf("invalid %s %q (want YYYY-MM-DD or RFC3339)", flagName, raw)}
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
