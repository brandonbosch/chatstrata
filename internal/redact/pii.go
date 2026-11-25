package redact

import (
	"encoding/json"
	"net/netip"
	"os/user"
	"regexp"
	"strings"
)

var piiRuleIDs = []string{
	"pii-email",
	"pii-ip",
	"pii-home-path",
	"pii-username",
	"pii-hostname",
}

var (
	emailPattern       = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,63}\b`)
	ipv4Pattern        = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	ipv6Pattern        = regexp.MustCompile(`(?i)[0-9a-f:]*:[0-9a-f:]+(?:%[0-9a-z_.-]+)?`)
	mappedPattern      = regexp.MustCompile(`(?i)::ffff:(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	unixHomePattern    = regexp.MustCompile(`(?:/home/|/Users/)([A-Za-z0-9._-]+)`)
	winHomePattern     = regexp.MustCompile(`(?i)[A-Z]:\\Users\\([^\\\r\n"'<>|]+)`)
	winHomeJSONPattern = regexp.MustCompile(`(?i)[A-Z]:\\\\Users\\\\([^\\\r\n"'<>|]+)`)
	privateHostPattern = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:local|internal|lan|home|corp)\b`)
)

type matcher struct {
	usernames    []string
	privateHosts []string
	rules        []compiledRule
	dicts        []compiledDictionary
}

func newMatcher(p Policy, reserved map[string]struct{}) (*matcher, error) {
	m := &matcher{}
	m.usernames = resolveUsernames(p)
	m.privateHosts = resolveHosts(p)
	used := make(map[string]struct{}, len(reserved))
	for id := range reserved {
		used[id] = struct{}{}
	}
	rules, err := compileUserRules(p.Rules, used)
	if err != nil {
		return nil, err
	}
	dicts, err := compileDictionaries(p.Dictionaries, used)
	if err != nil {
		return nil, err
	}
	m.rules = rules
	m.dicts = dicts
	return m, nil
}

func resolveUsernames(p Policy) []string {
	var out []string
	if !p.DisableLocalUsername {
		if u, err := user.Current(); err == nil {
			out = addUsername(out, u.Username)
		}
	}
	for _, name := range p.Usernames {
		out = addUsername(out, name)
	}
	return out
}

var commonUsernames = map[string]struct{}{
	"root": {}, "admin": {}, "administrator": {}, "user": {}, "guest": {},
	"public": {}, "shared": {}, "default": {}, "runner": {}, "ubuntu": {},
	"ec2-user": {}, "system": {}, "daemon": {}, "nobody": {},
}

func addUsername(out []string, raw string) []string {
	name := strings.TrimSpace(raw)
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	if len(name) < 3 {
		return out
	}
	if _, skip := commonUsernames[strings.ToLower(name)]; skip {
		return out
	}
	for _, existing := range out {
		if strings.EqualFold(existing, name) {
			return out
		}
	}
	return append(out, name)
}

func resolveHosts(p Policy) []string {
	var out []string
	for _, host := range p.PrivateHosts {
		out = addHost(out, host)
	}
	return out
}

func addHost(out []string, raw string) []string {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" || host == "localhost" || host == "localhost.localdomain" {
		return out
	}
	for _, existing := range out {
		if existing == host {
			return out
		}
	}
	return append(out, host)
}

func (m *matcher) versionKey() string {
	b, _ := json.Marshal(struct {
		Usernames    []string
		PrivateHosts []string
	}{m.usernames, m.privateHosts})
	return string(b)
}

func (m *matcher) ruleIDs() []string {
	out := append([]string(nil), piiRuleIDs...)
	for _, r := range m.rules {
		out = append(out, r.id)
	}
	for _, d := range m.dicts {
		out = append(out, d.id)
	}
	return out
}

func (m *matcher) detect(text string, scope Scope) []Finding {
	out := m.detectPII(text)
	for _, r := range m.rules {
		if scopeMatches(r.projects, r.paths, scope) {
			out = append(out, r.detect(text)...)
		}
	}
	for _, d := range m.dicts {
		if scopeMatches(d.projects, d.paths, scope) {
			out = append(out, d.detect(text)...)
		}
	}
	return out
}

func (m *matcher) detectPII(text string) []Finding {
	var out []Finding
	out = append(out, detectEmails(text)...)
	out = append(out, detectIPs(text)...)
	out = append(out, detectHomePaths(text)...)
	out = append(out, detectHosts(text, m.privateHosts)...)
	out = append(out, detectUsernames(text, m.usernames)...)
	return out
}

func detectEmails(text string) []Finding {
	var out []Finding
	for _, loc := range emailPattern.FindAllStringIndex(text, -1) {
		if !validEmail(text[loc[0]:loc[1]]) {
			continue
		}
		out = append(out, Finding{Start: loc[0], End: loc[1], RuleID: "pii-email", Category: "EMAIL", priority: 70})
	}
	return out
}

func validEmail(email string) bool {
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at >= len(email)-1 {
		return false
	}
	local, domain := email[:at], email[at+1:]
	if len(local) > 64 || len(domain) > 255 {
		return false
	}
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	tld := strings.ToLower(labels[len(labels)-1])
	if len(tld) < 2 {
		return false
	}
	for i := range len(tld) {
		if tld[i] < 'a' || tld[i] > 'z' {
			return false
		}
	}
	return true
}

func detectIPs(text string) []Finding {
	var out []Finding
	for _, loc := range ipv4Pattern.FindAllStringIndex(text, -1) {
		addr, err := netip.ParseAddr(text[loc[0]:loc[1]])
		if err != nil || !addr.Is4() || trivialIP(addr) {
			continue
		}
		out = append(out, Finding{Start: loc[0], End: loc[1], RuleID: "pii-ip", Category: "IP", priority: 60})
	}
	mapped := mappedPattern.FindAllStringIndex(text, -1)
	for _, loc := range mapped {
		addr, err := netip.ParseAddr(text[loc[0]:loc[1]])
		if err != nil || trivialIP(addr) {
			continue
		}
		out = append(out, Finding{Start: loc[0], End: loc[1], RuleID: "pii-ip", Category: "IP", priority: 60})
	}
	for _, loc := range ipv6Pattern.FindAllStringIndex(text, -1) {
		if insideAny(loc, mapped) {
			continue
		}
		raw := strings.TrimRight(text[loc[0]:loc[1]], ":")
		if raw == "" {
			continue
		}
		end := loc[0] + len(raw)
		addrPart := raw
		if i := strings.IndexByte(addrPart, '%'); i >= 0 {
			addrPart = addrPart[:i]
		}
		addr, err := netip.ParseAddr(addrPart)
		if err != nil || !addr.Is6() || trivialIP(addr) {
			continue
		}
		out = append(out, Finding{Start: loc[0], End: end, RuleID: "pii-ip", Category: "IP", priority: 60})
	}
	return out
}

func trivialIP(addr netip.Addr) bool {
	return addr.IsUnspecified() || addr.IsLoopback()
}

func insideAny(loc []int, spans [][]int) bool {
	for _, span := range spans {
		if loc[0] >= span[0] && loc[1] <= span[1] {
			return true
		}
	}
	return false
}

func detectHomePaths(text string) []Finding {
	var out []Finding
	for _, re := range []*regexp.Regexp{unixHomePattern, winHomePattern, winHomeJSONPattern} {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			if len(m) < 4 || m[2] < 0 {
				continue
			}
			start, end := m[2], m[3]
			for start < end && (text[start] == ' ' || text[start] == '\t') {
				start++
			}
			for end > start && (text[end-1] == ' ' || text[end-1] == '\t') {
				end--
			}
			if start >= end || skipHomeUser(text[start:end]) {
				continue
			}
			out = append(out, Finding{Start: start, End: end, RuleID: "pii-home-path", Category: "USERNAME", priority: 68})
		}
	}
	return out
}

func skipHomeUser(name string) bool {
	if name == "." || name == ".." {
		return true
	}
	switch strings.ToLower(name) {
	case "shared", "public", "default", "default user", "all users":
		return true
	}
	return false
}

func detectHosts(text string, privateHosts []string) []Finding {
	var out []Finding
	for _, loc := range privateHostPattern.FindAllStringIndex(text, -1) {
		if loc[1] < len(text) && text[loc[1]] == '.' && loc[1]+1 < len(text) && isHostByte(text[loc[1]+1]) {
			continue
		}
		out = append(out, Finding{Start: loc[0], End: loc[1], RuleID: "pii-hostname", Category: "HOSTNAME", priority: 55})
	}
	for _, host := range privateHosts {
		out = append(out, findHostLiteral(text, host)...)
	}
	return out
}

func detectUsernames(text string, usernames []string) []Finding {
	var out []Finding
	for _, name := range usernames {
		term := literalTerm{value: name, fold: isASCII(name)}
		out = append(out, findLiteral(text, term, "pii-username", "USERNAME", 66, true)...)
	}
	return out
}
