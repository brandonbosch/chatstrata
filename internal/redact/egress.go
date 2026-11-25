package redact

import (
	"errors"
	"fmt"
	"strings"
)

// ErrProjectDenied means the egress policy does not allow this project.
// Callers must not send any text for it.
var ErrProjectDenied = errors.New("project denied by redaction policy")

// GateOptions configures the project boundary in front of the redaction
// engine. AllowProjects and ExcludeProjects are exact names; an empty
// AllowProjects allows every project not excluded.
type GateOptions struct {
	AllowProjects   []string
	ExcludeProjects []string
}

// Gate is the egress boundary for labeling: it refuses denied projects and
// redacts every string field before it can leave the machine.
type Gate struct {
	engine  *Engine
	allow   map[string]struct{}
	exclude map[string]struct{}
}

// NewGate builds a gate with a compiled engine.
func NewGate(p Policy, opts GateOptions) (*Gate, error) {
	e, err := NewEngine(p)
	if err != nil {
		return nil, err
	}
	return &Gate{
		engine:  e,
		allow:   nameSet(opts.AllowProjects),
		exclude: nameSet(opts.ExcludeProjects),
	}, nil
}

func nameSet(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = struct{}{}
		}
	}
	return set
}

// Engine returns the underlying engine, for callers that need its rule list.
func (g *Gate) Engine() *Engine { return g.engine }

// Version identifies the policy applied by this gate.
func (g *Gate) Version() string { return g.engine.Version() }

// CheckProject reports whether the project may leave the machine.
func (g *Gate) CheckProject(project string) error {
	project = strings.TrimSpace(project)
	if _, ok := g.exclude[project]; ok {
		return fmt.Errorf("%w: %q is excluded", ErrProjectDenied, project)
	}
	if len(g.allow) > 0 {
		if _, ok := g.allow[project]; !ok {
			return fmt.Errorf("%w: %q is not allowed", ErrProjectDenied, project)
		}
	}
	return nil
}

// Redact checks the project and redacts one value.
func (g *Gate) Redact(project, text string) (Result, error) {
	return g.RedactScope(Scope{Project: project}, text)
}

// RedactScope checks the scope's project and redacts one value with project
// and path rules applied.
func (g *Gate) RedactScope(scope Scope, text string) (Result, error) {
	if err := g.CheckProject(scope.Project); err != nil {
		return Result{}, err
	}
	return g.engine.RedactScope(scope, text)
}

// Report summarizes a field-level redaction without carrying any text.
type Report struct {
	Version  string         `json:"redaction_version"`
	Fields   int            `json:"fields"`
	Findings int            `json:"findings"`
	Counts   map[string]int `json:"counts,omitempty"`
}

// RedactFields checks the project and redacts every string value. It returns a
// new map; the input is not modified. On any error no partial map is returned,
// so a caller cannot accidentally send a half-sanitized record.
func (g *Gate) RedactFields(project string, fields map[string]string) (map[string]string, Report, error) {
	return g.RedactFieldsScope(Scope{Project: project}, fields)
}

// RedactFieldsScope is RedactFields with a project and source path scope.
func (g *Gate) RedactFieldsScope(scope Scope, fields map[string]string) (map[string]string, Report, error) {
	if err := g.CheckProject(scope.Project); err != nil {
		return nil, Report{}, err
	}
	out := make(map[string]string, len(fields))
	report := Report{Version: g.engine.Version(), Fields: len(fields)}
	for name, value := range fields {
		result, err := g.engine.RedactScope(scope, value)
		if err != nil {
			return nil, Report{}, fmt.Errorf("redact field %q: %w", name, err)
		}
		out[name] = result.Text
		report.Findings += len(result.Findings)
		for rule, n := range result.Counts {
			if report.Counts == nil {
				report.Counts = make(map[string]int)
			}
			report.Counts[rule] += n
		}
	}
	return out, report, nil
}
