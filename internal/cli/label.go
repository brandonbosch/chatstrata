package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/brandonbosch/chatstrata/v2/internal/label"
)

func runLabel(e *env, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprintln(e.stdout, "Usage: chatstrata label SUBCOMMAND [ARGS]...")
		_, _ = fmt.Fprintln(e.stdout)
		_, _ = fmt.Fprintln(e.stdout, "Subcommands:")
		_, _ = fmt.Fprintln(e.stdout, "  packs    List built-in and user classifier packs")
		_, _ = fmt.Fprintln(e.stdout, "  run      Run one pack (or --all) through the local redaction gate")
		_, _ = fmt.Fprintln(e.stdout, "  summary  Summarize a pack's stored labels")
		_, _ = fmt.Fprintln(e.stdout, "  runs     List recorded classifier runs")
		_, _ = fmt.Fprintln(e.stdout, "  clear    Delete a pack's labels from the sidecar log and archive")
		return nil
	}
	switch args[0] {
	case "packs":
		return runLabelPacks(e, args[1:])
	case "run":
		return runLabelRun(e, args[1:])
	case "summary":
		return runLabelSummary(e, args[1:])
	case "runs":
		return runLabelRuns(e, args[1:])
	case "clear":
		return runLabelClear(e, args[1:])
	default:
		return usageError{msg: fmt.Sprintf("unknown label subcommand %q (want packs, run, summary, runs, or clear)", args[0])}
	}
}

func runLabelPacks(e *env, args []string) error {
	fs := newFlagSet(e, "label packs", "label packs [--packs-dir DIR] [--json]")
	packsDir := fs.String("packs-dir", "", "Additional TOML packs directory (default: $CHATSTRATA_PACKS_DIR, then <config>/chatstrata/packs).")
	asJSON := fs.Bool("json", false, "Output pack definitions as JSON.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	packs, err := label.LoadPacks(*packsDir)
	if err != nil {
		return err
	}
	ordered, err := label.TopologicalOrder(packs)
	if err != nil {
		return err
	}
	if *asJSON {
		type packJSON struct {
			Name        string                        `json:"name"`
			Description string                        `json:"description"`
			TargetKind  string                        `json:"target_kind"`
			Version     string                        `json:"version"`
			DependsOn   []string                      `json:"depends_on,omitempty"`
			Source      string                        `json:"source"`
			Questions   map[string]label.QuestionSpec `json:"questions"`
		}
		out := make([]packJSON, 0, len(ordered))
		for _, p := range ordered {
			out = append(out, packJSON{
				Name:        p.Name,
				Description: p.Description,
				TargetKind:  p.TargetKind,
				Version:     p.Version(),
				DependsOn:   p.DependsOn,
				Source:      p.Source,
				Questions:   p.Questions,
			})
		}
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	for _, p := range ordered {
		deps := ""
		if len(p.DependsOn) > 0 {
			deps = ", depends_on: " + strings.Join(p.DependsOn, ", ")
		}
		_, _ = fmt.Fprintf(e.stdout, "%s  (%s, version %s%s)\n", p.Name, p.TargetKind, p.Version(), deps)
		_, _ = fmt.Fprintf(e.stdout, "  %s\n", strings.Join(strings.Fields(p.Description), " "))
		qids := make([]string, 0, len(p.Questions))
		for qid := range p.Questions {
			qids = append(qids, qid)
		}
		sort.Strings(qids)
		for _, qid := range qids {
			_, _ = fmt.Fprintf(e.stdout, "  - %s [%s]\n", qid, p.Questions[qid].Type)
		}
		_, _ = fmt.Fprintln(e.stdout)
	}
	return nil
}

func runLabelSummary(e *env, args []string) error {
	fs := newFlagSet(e, "label summary", "label summary PACK [--by DIM] [--min-confidence F] [--packs-dir DIR] [--json] [--db PATH]")
	by := fs.String("by", "", "Group by dimension (source, project, month, quarter; plus tool, model for tool_call).")
	minConf := fs.Float64("min-confidence", 0.0, "Ignore choice/score answers below this confidence.")
	packsDir := fs.String("packs-dir", "", "Additional TOML packs directory.")
	asJSON := fs.Bool("json", false, "Output as JSON.")
	db := fs.String("db", "", "Override the database path.")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{msg: "label summary requires a PACK name"}
	}
	pack, err := resolvePack(*packsDir, pos[0])
	if err != nil {
		return err
	}
	s, err := openForReading(e, *db)
	if s == nil || err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	rows, err := label.Summarize(e.ctx, s, pack, *by, *minConf)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	if len(rows) == 0 {
		_, _ = fmt.Fprintf(e.stdout, "No labels stored for %s.\n", pack.Name)
		return nil
	}
	_, _ = fmt.Fprintf(e.stdout, "%-16s %-26s %-22s %6s %8s %6s %8s\n",
		"group", "question", "answer", "n", "share%", "mean", "avg_conf")
	for _, r := range rows {
		_, _ = fmt.Fprintf(e.stdout, "%-16s %-26s %-22s %6d %8s %6s %8s\n",
			r.Group, r.Question, r.Answer, r.Count,
			fmtOptFloat(r.SharePct, "%.1f"), fmtOptFloat(r.Mean, "%.2f"), fmtOptFloat(r.AvgConf, "%.2f"))
	}
	return nil
}

func runLabelRuns(e *env, args []string) error {
	fs := newFlagSet(e, "label runs", "label runs [--pack NAME] [--json] [--db PATH]")
	packFilter := fs.String("pack", "", "Only list runs for this pack.")
	asJSON := fs.Bool("json", false, "Output as JSON.")
	db := fs.String("db", "", "Override the database path.")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	s, err := openForReading(e, *db)
	if s == nil || err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	runs, err := label.ListRuns(e.ctx, s, *packFilter)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(runs)
	}
	if len(runs) == 0 {
		_, _ = fmt.Fprintln(e.stdout, "No label runs recorded.")
		return nil
	}
	_, _ = fmt.Fprintf(e.stdout, "%-36s %-22s %-12s %-12s %7s %5s %10s\n",
		"run_id", "pack", "version", "model", "targets", "fail", "in_tokens")
	for _, r := range runs {
		_, _ = fmt.Fprintf(e.stdout, "%-36s %-22s %-12s %-12s %7d %5d %10d\n",
			r.RunID, r.Pack, r.PackVersion, r.Model, r.Targets, r.Failures, r.InputTokens)
	}
	return nil
}

func runLabelClear(e *env, args []string) error {
	fs := newFlagSet(e, "label clear", "label clear PACK [--version V] [--run ID] --yes [--db PATH]")
	version := fs.String("version", "", "Only clear runs matching this pack_version.")
	runID := fs.String("run", "", "Only clear this specific run_id.")
	yes := fs.Bool("yes", false, "Confirm deleting matching label runs from the sidecar log and archive.")
	db := fs.String("db", "", "Override the database path.")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{msg: "label clear requires a PACK name"}
	}
	if !*yes {
		return usageError{msg: "label clear requires --yes (deletes durable run files from chatstrata-go.labels/)"}
	}
	a, err := openArchive(e, *db)
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()

	removed, err := label.Clear(e.ctx, a.store, label.Dir(a.store.Path), label.ClearFilter{
		Pack:        pos[0],
		PackVersion: *version,
		RunID:       *runID,
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.stdout, "Cleared %d run(s) for %s.\n", removed, pos[0])
	return nil
}

func resolvePack(packsDir, name string) (*label.Pack, error) {
	packs, err := label.LoadPacks(packsDir)
	if err != nil {
		return nil, err
	}
	p, ok := packs[name]
	if !ok {
		known := make([]string, 0, len(packs))
		for k := range packs {
			known = append(known, k)
		}
		sort.Strings(known)
		return nil, usageError{msg: fmt.Sprintf("unknown pack %q (available: %s)", name, strings.Join(known, ", "))}
	}
	return p, nil
}

func fmtOptFloat(v *float64, layout string) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf(layout, *v)
}
