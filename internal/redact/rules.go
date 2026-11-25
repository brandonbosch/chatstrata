package redact

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

const (
	maxUserRules    = 100
	maxDictionaries = 100
	maxDictTerms    = 1000
	maxPatternBytes = 4096
	maxCategoryLen  = 64
)

type compiledRule struct {
	id       string
	category string
	re       *regexp.Regexp
	group    int
	priority int

	entropy   float64
	allowlist []string
	projects  []string
	paths     []string
}

func compileUserRules(rules []UserRule, used map[string]struct{}) ([]compiledRule, error) {
	if len(rules) > maxUserRules {
		return nil, fmt.Errorf("redaction policy has %d rules (limit %d)", len(rules), maxUserRules)
	}
	out := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		if err := validateID(r.ID); err != nil {
			return nil, fmt.Errorf("redaction rule %q: %w", r.ID, err)
		}
		if _, ok := used[r.ID]; ok {
			return nil, fmt.Errorf("redaction rule %q: ID is already used by another rule", r.ID)
		}
		used[r.ID] = struct{}{}
		if r.Pattern == "" || len(r.Pattern) > maxPatternBytes {
			return nil, fmt.Errorf("redaction rule %q: pattern must be 1..%d bytes", r.ID, maxPatternBytes)
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("redaction rule %q: %w", r.ID, err)
		}
		if r.SecretGroup < 0 || r.SecretGroup > re.NumSubexp() {
			return nil, fmt.Errorf("redaction rule %q: secret_group %d is outside the pattern's %d groups", r.ID, r.SecretGroup, re.NumSubexp())
		}
		if r.Priority == 0 {
			r.Priority = 75
		}
		if r.Priority < 1 || r.Priority > 100 {
			return nil, fmt.Errorf("redaction rule %q: priority %d is outside 1..100", r.ID, r.Priority)
		}
		if r.Entropy < 0 || r.Entropy > 8 {
			return nil, fmt.Errorf("redaction rule %q: entropy %.2f is outside 0..8", r.ID, r.Entropy)
		}
		category, err := normalizeCategory(r.ID, r.Category)
		if err != nil {
			return nil, fmt.Errorf("redaction rule %q: %w", r.ID, err)
		}
		out = append(out, compiledRule{
			id:        r.ID,
			category:  category,
			re:        re,
			group:     r.SecretGroup,
			priority:  r.Priority,
			entropy:   r.Entropy,
			allowlist: cleanList(r.Allowlist, 100),
			projects:  cleanList(r.Projects, 100),
			paths:     cleanList(r.Paths, 100),
		})
	}
	return out, nil
}

func (r compiledRule) detect(text string) []Finding {
	var out []Finding
	for _, match := range r.re.FindAllStringSubmatchIndex(text, -1) {
		start, end := match[0], match[1]
		if r.group > 0 {
			pair := 2 * r.group
			if pair+1 >= len(match) || match[pair] < 0 {
				continue
			}
			start, end = match[pair], match[pair+1]
		}
		if start < 0 || end <= start {
			continue
		}
		value := text[start:end]
		if r.entropy > 0 && shannonEntropy(value) < r.entropy {
			continue
		}
		if allowlisted(value, r.allowlist) {
			continue
		}
		out = append(out, Finding{
			Start: start, End: end, RuleID: r.id, Category: r.category, priority: r.priority,
		})
	}
	return out
}

type compiledDictionary struct {
	id       string
	category string
	priority int
	terms    []literalTerm
	projects []string
	paths    []string
}

func compileDictionaries(dicts []Dictionary, used map[string]struct{}) ([]compiledDictionary, error) {
	if len(dicts) > maxDictionaries {
		return nil, fmt.Errorf("redaction policy has %d dictionaries (limit %d)", len(dicts), maxDictionaries)
	}
	out := make([]compiledDictionary, 0, len(dicts))
	for _, d := range dicts {
		if err := validateID(d.ID); err != nil {
			return nil, fmt.Errorf("redaction dictionary %q: %w", d.ID, err)
		}
		if _, ok := used[d.ID]; ok {
			return nil, fmt.Errorf("redaction dictionary %q: ID is already used by another rule", d.ID)
		}
		used[d.ID] = struct{}{}
		if len(d.Terms) == 0 || len(d.Terms) > maxDictTerms {
			return nil, fmt.Errorf("redaction dictionary %q: terms must be 1..%d", d.ID, maxDictTerms)
		}
		if d.Priority == 0 {
			d.Priority = 74
		}
		if d.Priority < 1 || d.Priority > 100 {
			return nil, fmt.Errorf("redaction dictionary %q: priority %d is outside 1..100", d.ID, d.Priority)
		}
		category, err := normalizeCategory(d.ID, d.Category)
		if err != nil {
			return nil, fmt.Errorf("redaction dictionary %q: %w", d.ID, err)
		}
		terms := make([]literalTerm, 0, len(d.Terms))
		seen := make(map[string]struct{}, len(d.Terms))
		for _, raw := range d.Terms {
			term := strings.TrimSpace(raw)
			if len(term) < 2 || len(term) > 200 {
				return nil, fmt.Errorf("redaction dictionary %q: term %q must be 2..200 bytes", d.ID, term)
			}
			key := strings.ToLower(term)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			terms = append(terms, literalTerm{value: term, fold: !d.CaseSensitive && isASCII(term)})
		}
		out = append(out, compiledDictionary{
			id: d.ID, category: category, priority: d.Priority, terms: terms,
			projects: cleanList(d.Projects, 100), paths: cleanList(d.Paths, 100),
		})
	}
	return out, nil
}

func (d compiledDictionary) detect(text string) []Finding {
	var out []Finding
	for _, term := range d.terms {
		out = append(out, findLiteral(text, term, d.id, d.category, d.priority, true)...)
	}
	return out
}

type literalTerm struct {
	value string
	fold  bool
}

func findLiteral(text string, term literalTerm, id, category string, priority int, wordBoundary bool) []Finding {
	if term.value == "" {
		return nil
	}
	var out []Finding
	for from := 0; from+len(term.value) <= len(text); {
		i := indexTerm(text[from:], term)
		if i < 0 {
			break
		}
		start := from + i
		end := start + len(term.value)
		if !wordBoundary || wordBoundaryOK(text, start, end) {
			out = append(out, Finding{Start: start, End: end, RuleID: id, Category: category, priority: priority})
			from = end
		} else {
			from = start + 1
		}
	}
	return out
}

func findHostLiteral(text, host string) []Finding {
	if host == "" {
		return nil
	}
	var out []Finding
	for from := 0; from+len(host) <= len(text); {
		i := indexFoldASCII(text[from:], host)
		if i < 0 {
			break
		}
		start := from + i
		end := start + len(host)
		if hostBoundaryOK(text, start, end) {
			out = append(out, Finding{Start: start, End: end, RuleID: "pii-hostname", Category: "HOSTNAME", priority: 55})
			from = end
		} else {
			from = start + 1
		}
	}
	return out
}

func indexTerm(text string, term literalTerm) int {
	if term.fold {
		return indexFoldASCII(text, term.value)
	}
	return strings.Index(text, term.value)
}

func wordBoundaryOK(text string, start, end int) bool {
	if start > 0 && isWordByte(text[start-1]) && isWordByte(text[start]) {
		return false
	}
	if end < len(text) && isWordByte(text[end]) && isWordByte(text[end-1]) {
		return false
	}
	return true
}

func hostBoundaryOK(text string, start, end int) bool {
	if start > 0 && isHostByte(text[start-1]) && isHostByte(text[start]) {
		return false
	}
	if end < len(text) && isHostByte(text[end]) && isHostByte(text[end-1]) {
		return false
	}
	return true
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

func isHostByte(b byte) bool {
	return isWordByte(b) || b == '.' || b == '-'
}

func scopeMatches(projects, paths []string, scope Scope) bool {
	project := strings.TrimSpace(scope.Project)
	if len(projects) > 0 {
		if project == "" {
			return false
		}
		found := false
		for _, p := range projects {
			if project == p {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(paths) > 0 {
		if scope.Path == "" {
			return false
		}
		found := false
		for _, p := range paths {
			if strings.HasPrefix(scope.Path, p) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	var counts [256]int
	for i := range len(s) {
		counts[s[i]]++
	}
	var e float64
	n := float64(len(s))
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		e -= p * math.Log2(p)
	}
	return e
}

func allowlisted(value string, entries []string) bool {
	lower := strings.ToLower(value)
	for _, entry := range entries {
		if strings.Contains(lower, strings.ToLower(entry)) {
			return true
		}
	}
	return false
}

func validateID(id string) error {
	if id == "" || len(id) > 64 {
		return fmt.Errorf("id must be 1..64 characters")
	}
	for _, r := range id {
		if !isNameRune(r) {
			return fmt.Errorf("id %q may contain only letters, digits, '.', '_' and '-'", id)
		}
	}
	return nil
}

func normalizeCategory(id, category string) (string, error) {
	if category == "" {
		category = strings.ToUpper(id)
	}
	if len(category) > maxCategoryLen {
		return "", fmt.Errorf("category must be at most %d characters", maxCategoryLen)
	}
	for _, r := range category {
		if !isNameRune(r) {
			return "", fmt.Errorf("category %q may contain only letters, digits, '.', '_' and '-'", category)
		}
	}
	return category, nil
}

func isNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
}

func cleanList(in []string, limit int) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
