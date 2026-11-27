// Package redact is chatstrata's local redaction boundary (ADR 0008).
//
// It detects secrets in text and replaces them with fixed, typed placeholders
// such as [REDACTED:OPENAI_PROJECT_KEY]. Detection is local, deterministic and
// offline: no network, no cgo and no model files. Findings carry byte offsets
// and rule identifiers, never matched text.
//
// The package is intentionally the only caller of the secret detector. A
// labeling egress gate uses Gate; other packages use Engine directly.
package redact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/baum-ai/goredact"
)

// Engine and rule-set versions participate in Policy.Version. Change them
// when detection or replacement changes, so labels can tell sanitized text
// apart.
const (
	engineVersion       = "redact-2"
	supplementalVersion = "ai-2"
	piiVersion          = "pii-1"
	placeholderVersion  = "typed-1"
)

// DefaultMaxInputBytes bounds one redaction call. Labeling state is small;
// the cap keeps a pathological field from allocating a large output.
const DefaultMaxInputBytes = 1 << 20

// Profile trades detection depth for false positives. Balanced is the egress
// default; Deep is for audits and users who prefer over-redaction.
type Profile string

const (
	ProfileFast     Profile = "fast"
	ProfileBalanced Profile = "balanced"
	ProfileDeep     Profile = "deep"
)

// ParseProfile accepts the three named profiles, case-insensitively. Empty
// selects balanced.
func ParseProfile(s string) (Profile, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ProfileBalanced):
		return ProfileBalanced, nil
	case string(ProfileFast):
		return ProfileFast, nil
	case string(ProfileDeep):
		return ProfileDeep, nil
	default:
		return "", fmt.Errorf("unknown redaction profile %q (want fast, balanced or deep)", s)
	}
}

func (p Profile) goredact() (goredact.Profile, error) {
	switch p {
	case ProfileFast:
		return goredact.ProfileFast, nil
	case ProfileBalanced:
		return goredact.ProfileBalanced, nil
	case ProfileDeep:
		return goredact.ProfileDeep, nil
	default:
		return 0, fmt.Errorf("unknown redaction profile %q", p)
	}
}

// Policy configures an Engine. The zero value is valid and means
// DefaultPolicy. Usernames, PrivateHosts, Rules and Dictionaries extend the
// built-in recognizers; LoadPolicy populates them from TOML.
type Policy struct {
	Profile       Profile
	MaxInputBytes int
	// Collect redacts a new observation before it is appended to the log.
	// It permanently discards the source plaintext from this device's archive.
	Collect bool

	// Usernames are extra local usernames to redact. The current OS username
	// is added unless DisableLocalUsername is set.
	Usernames            []string
	DisableLocalUsername bool
	// PrivateHosts are extra hostnames to redact in addition to the built-in
	// .local/.internal/.lan/.home/.corp suffixes. The current hostname is
	// added automatically.
	PrivateHosts []string

	Rules        []UserRule
	Dictionaries []Dictionary
}

// DefaultPolicy is the egress default: balanced detection with a 1 MiB cap.
func DefaultPolicy() Policy {
	return Policy{Profile: ProfileBalanced, MaxInputBytes: DefaultMaxInputBytes}
}

func (p Policy) normalized() Policy {
	if p.Profile == "" {
		p.Profile = ProfileBalanced
	}
	if p.MaxInputBytes <= 0 {
		p.MaxInputBytes = DefaultMaxInputBytes
	}
	return p
}

// ErrInputTooLarge means text exceeded Policy.MaxInputBytes and was not
// scanned. Callers must not send the original text.
var ErrInputTooLarge = errors.New("redaction input exceeds limit")

// Finding is one redacted span. Start and End are byte offsets into the
// original text, half-open. RuleID is stable and never contains the match.
type Finding struct {
	Start    int
	End      int
	RuleID   string
	Category string

	priority int
}

// Result is a redacted copy plus payload-free metadata. Text is the output;
// Findings and Counts never contain the matched bytes.
type Result struct {
	Text     string
	Findings []Finding
	Counts   map[string]int
	Version  string
}

// Engine is a compiled, reusable redaction engine. It is safe for concurrent
// use; calls serialize on the detector's per-call finding buffer.
type Engine struct {
	mu       sync.Mutex
	det      *goredact.Engine
	matcher  *matcher
	policy   Policy
	version  string
	findings []Finding
}

// NewEngine compiles the configured profile and supplemental AI rules.
func NewEngine(p Policy) (*Engine, error) {
	p = p.normalized()
	gp, err := p.Profile.goredact()
	if err != nil {
		return nil, err
	}
	e := &Engine{policy: p}
	det, err := goredact.New(goredact.Config{
		Profile: gp,
		OnFinding: func(f goredact.Finding) {
			e.findings = append(e.findings, Finding{
				Start:    int(f.Start),
				End:      int(f.End),
				RuleID:   f.RuleID,
				priority: builtinPriority(f.RuleID),
			})
		},
	})
	if err != nil {
		return nil, fmt.Errorf("compile redaction rules: %w", err)
	}
	e.det = det
	reserved := make(map[string]struct{})
	for _, r := range det.ActiveRules() {
		reserved[r.ID] = struct{}{}
	}
	for _, r := range supplementalPrefixRules {
		reserved[r.ID] = struct{}{}
	}
	for _, r := range contextualRules {
		reserved[r.ID] = struct{}{}
	}
	reserved["password-assignment"] = struct{}{}
	for _, id := range piiRuleIDs {
		reserved[id] = struct{}{}
	}
	m, err := newMatcher(p, reserved)
	if err != nil {
		return nil, err
	}
	e.matcher = m
	e.version = policyVersion(p, det.RuleSetVersion(), m.versionKey())
	return e, nil
}

// Policy returns the normalized policy.
func (e *Engine) Policy() Policy { return e.policy }

// Version identifies the rule set and replacement format. Record it with any
// text derived from Redact.
func (e *Engine) Version() string { return e.version }

// RuleIDs lists active built-in and supplemental rule identifiers, sorted.
func (e *Engine) RuleIDs() []string {
	ids := make(map[string]struct{})
	for _, r := range e.det.ActiveRules() {
		ids[r.ID] = struct{}{}
	}
	for _, r := range supplementalPrefixRules {
		ids[r.ID] = struct{}{}
	}
	for _, r := range contextualRules {
		ids[r.ID] = struct{}{}
	}
	ids["password-assignment"] = struct{}{}
	for _, id := range e.matcher.ruleIDs() {
		ids[id] = struct{}{}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Redact scans text with no project/path scope and returns sanitized text.
// Rules and dictionaries that require a project or path do not run; use
// RedactScope to give them their scope.
func (e *Engine) Redact(text string) (Result, error) {
	return e.RedactScope(Scope{}, text)
}

// RedactContext is Redact with cancellation.
func (e *Engine) RedactContext(ctx context.Context, text string) (Result, error) {
	return e.RedactScopeContext(ctx, Scope{}, text)
}

// RedactScope scans text for a conversation's project and source path.
func (e *Engine) RedactScope(scope Scope, text string) (Result, error) {
	return e.RedactScopeContext(context.Background(), scope, text)
}

// RedactScopeContext is RedactScope with cancellation. On error no Result is
// returned; callers must treat the text as unsent.
func (e *Engine) RedactScopeContext(ctx context.Context, scope Scope, text string) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(text) > e.policy.MaxInputBytes {
		return Result{}, fmt.Errorf("%w: %d bytes (limit %d)", ErrInputTooLarge, len(text), e.policy.MaxInputBytes)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.findings = e.findings[:0]
	if _, err := e.det.Redact(ctx, io.Discard, strings.NewReader(text)); err != nil {
		return Result{}, fmt.Errorf("detect secrets: %w", err)
	}
	findings := append([]Finding(nil), e.findings...)
	findings = append(findings, detectSupplemental(text)...)
	findings = append(findings, e.matcher.detect(text, scope)...)
	findings = mergeFindings(findings, len(text))

	if len(findings) == 0 {
		return Result{Text: text, Version: e.version}, nil
	}

	counts := make(map[string]int, len(findings))
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, f := range findings {
		b.WriteString(text[last:f.Start])
		b.WriteString(placeholderFor(f))
		last = f.End
		counts[f.RuleID]++
	}
	b.WriteString(text[last:])
	return Result{Text: b.String(), Findings: findings, Counts: counts, Version: e.version}, nil
}

func policyVersion(p Policy, builtinVersion, matcherVersion string) string {
	// Collect changes where redaction is applied, not what it produces; two
	// devices with the same policy must report the same version whether one
	// collects redacted and the other only applies the overlay.
	p.Collect = false
	config, _ := json.Marshal(p)
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\n%s\n%s\n%s\n%s\n%s\n%s",
		engineVersion, p.Profile, builtinVersion, supplementalVersion, piiVersion,
		placeholderVersion, config)
	_, _ = fmt.Fprintf(h, "\n%s", matcherVersion)
	return "rd2-" + hex.EncodeToString(h.Sum(nil)[:8])
}

func builtinPriority(ruleID string) int {
	switch {
	case strings.Contains(ruleID, "private-key"):
		return 100
	case strings.HasPrefix(ruleID, "generic-"):
		return 40
	default:
		return 80
	}
}

// mergeFindings drops invalid spans, resolves overlaps by priority then
// length, and returns spans in input order. goredact already emits
// non-overlapping findings; supplemental rules can overlap a generic finding.
func mergeFindings(in []Finding, textLen int) []Finding {
	valid := make([]Finding, 0, len(in))
	for _, f := range in {
		if f.Start < 0 || f.End <= f.Start || f.End > textLen {
			continue
		}
		valid = append(valid, f)
	}
	sort.SliceStable(valid, func(i, j int) bool {
		a, b := valid[i], valid[j]
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.priority != b.priority {
			return a.priority > b.priority
		}
		if a.End != b.End {
			return a.End > b.End
		}
		return a.RuleID < b.RuleID
	})

	out := make([]Finding, 0, len(valid))
	for _, f := range valid {
		if len(out) == 0 || f.Start >= out[len(out)-1].End {
			out = append(out, f)
			continue
		}
		if betterFinding(f, out[len(out)-1]) {
			out[len(out)-1] = f
		}
	}
	return out
}

func betterFinding(a, b Finding) bool {
	aContainsB := a.Start <= b.Start && a.End >= b.End
	bContainsA := b.Start <= a.Start && b.End >= a.End
	switch {
	case aContainsB && !bContainsA:
		return true
	case bContainsA && !aContainsB:
		return false
	}
	if a.priority != b.priority {
		return a.priority > b.priority
	}
	if a.End-a.Start != b.End-b.Start {
		return a.End-a.Start > b.End-b.Start
	}
	return a.RuleID < b.RuleID
}

func placeholderFor(f Finding) string {
	name := f.Category
	if name == "" {
		name = f.RuleID
	}
	return placeholder(name)
}

// placeholder renders a fixed typed marker. Length and token shape are not
// preserved: both disclose information about the original secret.
func placeholder(ruleID string) string {
	var b strings.Builder
	b.WriteString("[REDACTED:")
	lastUnderscore := false
	for _, r := range strings.ToUpper(ruleID) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore && b.Len() > len("[REDACTED:") {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	out := strings.TrimSuffix(b.String(), "_") + "]"
	if out == "[REDACTED:]" {
		return "[REDACTED:SECRET]"
	}
	return out
}
