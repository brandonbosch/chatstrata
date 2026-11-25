package redact

import "strings"

// supplementalPrefixRules extend the compiled detector with provider formats
// that goredact does not ship: AI gateways, search APIs, chatstrata's own age
// key, and a few adjacent credentials. Each rule is a literal prefix followed
// by a bounded token body; the prefix carries the provider identity, so the
// body can be validated without context.
type prefixRule struct {
	ID      string
	Prefix  string
	MinBody int
	MaxBody int
	Kind    bodyKind
}

type bodyKind uint8

const (
	bodyToken bodyKind = iota
	bodyLowerHex
	bodyUpperAlnum
)

func (r prefixRule) accepts(b byte) bool {
	switch r.Kind {
	case bodyLowerHex:
		return b >= '0' && b <= '9' || b >= 'a' && b <= 'f'
	case bodyUpperAlnum:
		return b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
	default:
		return isTokenByte(b)
	}
}

var supplementalPrefixRules = []prefixRule{
	{ID: "openrouter-api-key", Prefix: "sk-or-v1-", MinBody: 64, MaxBody: 64, Kind: bodyLowerHex},
	{ID: "xai-api-key", Prefix: "xai-", MinBody: 32, MaxBody: 200},
	{ID: "perplexity-api-key", Prefix: "pplx-", MinBody: 32, MaxBody: 200},
	{ID: "fireworks-api-key", Prefix: "fw_", MinBody: 32, MaxBody: 200},
	{ID: "fireworks-firepass-key", Prefix: "fpk_", MinBody: 32, MaxBody: 200},
	{ID: "tavily-api-key", Prefix: "tvly-", MinBody: 32, MaxBody: 200},
	{ID: "replicate-api-token", Prefix: "r8_", MinBody: 32, MaxBody: 200},
	{ID: "nvidia-api-key", Prefix: "nvapi-", MinBody: 40, MaxBody: 200},
	{ID: "jina-api-key", Prefix: "jina_", MinBody: 40, MaxBody: 200},
	{ID: "age-secret-key", Prefix: "AGE-SECRET-KEY-PQ-1", MinBody: 50, MaxBody: 120, Kind: bodyUpperAlnum},
	{ID: "age-secret-key", Prefix: "AGE-SECRET-KEY-1", MinBody: 50, MaxBody: 90, Kind: bodyUpperAlnum},
	{ID: "google-oauth-client-secret", Prefix: "GOCSPX-", MinBody: 20, MaxBody: 100},
	{ID: "databricks-api-token", Prefix: "dapi", MinBody: 32, MaxBody: 40, Kind: bodyLowerHex},
}

// contextualRules cover providers whose keys have no distinctive prefix. The
// variable name provides the identity; the validator then accepts only a
// plausible token value and rejects obvious placeholders.
type contextualRule struct {
	ID      string
	Names   []string
	MinBody int
	MaxBody int
	Kind    bodyKind
}

func (r contextualRule) accepts(b byte) bool {
	return prefixRule{Kind: r.Kind}.accepts(b)
}

var contextualRules = []contextualRule{
	{ID: "mistral-api-key", Names: []string{"MISTRAL_API_KEY"}, MinBody: 32, MaxBody: 64},
	{ID: "together-api-key", Names: []string{"TOGETHER_API_KEY"}, MinBody: 40, MaxBody: 80, Kind: bodyLowerHex},
	{ID: "azure-openai-api-key", Names: []string{"AZURE_OPENAI_API_KEY"}, MinBody: 32, MaxBody: 64, Kind: bodyLowerHex},
}

func detectSupplemental(text string) []Finding {
	var out []Finding
	for _, r := range supplementalPrefixRules {
		out = append(out, findPrefixed(text, r)...)
	}
	for _, r := range contextualRules {
		out = append(out, findAssignment(text, r)...)
	}
	out = append(out, findPasswordAssignments(text)...)
	return out
}

func findPrefixed(text string, r prefixRule) []Finding {
	var out []Finding
	for from := 0; from < len(text); {
		i := strings.Index(text[from:], r.Prefix)
		if i < 0 {
			break
		}
		start := from + i
		body := start + len(r.Prefix)
		if start > 0 && isIdentByte(text[start-1]) {
			from = start + 1
			continue
		}
		end := body
		for end < len(text) && r.accepts(text[end]) {
			end++
		}
		n := end - body
		if n < r.MinBody || n > r.MaxBody {
			from = start + 1
			continue
		}
		if end < len(text) && isIdentByte(text[end]) {
			from = end
			continue
		}
		out = append(out, Finding{Start: start, End: end, RuleID: r.ID, priority: 90})
		from = end
	}
	return out
}

func findAssignment(text string, r contextualRule) []Finding {
	var out []Finding
	for _, name := range r.Names {
		for from := 0; from < len(text); {
			i := indexFoldASCII(text[from:], name)
			if i < 0 {
				break
			}
			start := from + i
			if start > 0 && isIdentByte(text[start-1]) {
				from = start + 1
				continue
			}
			pos := start + len(name)
			for pos < len(text) && (text[pos] == ' ' || text[pos] == '\t') {
				pos++
			}
			if pos >= len(text) || (text[pos] != '=' && text[pos] != ':') {
				from = start + len(name)
				continue
			}
			pos++
			for pos < len(text) && (text[pos] == ' ' || text[pos] == '\t') {
				pos++
			}
			quote := byte(0)
			if pos < len(text) && (text[pos] == '"' || text[pos] == '\'') {
				quote = text[pos]
				pos++
			}
			valueStart := pos
			for pos < len(text) && r.accepts(text[pos]) {
				pos++
			}
			valueEnd := pos
			if quote != 0 {
				if pos >= len(text) || text[pos] != quote {
					from = start + len(name)
					continue
				}
			}
			n := valueEnd - valueStart
			if n < r.MinBody || n > r.MaxBody || !plausibleToken(text[valueStart:valueEnd]) {
				from = start + len(name)
				continue
			}
			out = append(out, Finding{Start: valueStart, End: valueEnd, RuleID: r.ID, priority: 90})
			from = valueEnd
		}
	}
	return out
}

// passwordTriggers catch the MVP's password case. goredact rejects
// low-entropy and passphrase-shaped values as prose; for an egress boundary a
// quoted value after a password assignment is a secret even when it is weak.
var passwordTriggers = []string{"password", "passwd", "pwd"}

func findPasswordAssignments(text string) []Finding {
	var out []Finding
	for _, trigger := range passwordTriggers {
		for from := 0; from < len(text); {
			i := indexFoldASCII(text[from:], trigger)
			if i < 0 {
				break
			}
			start := from + i
			pos := start + len(trigger)
			pos = skipHorizontal(text, pos)
			// JSON and YAML keys may close with a quote before the operator.
			if pos < len(text) && (text[pos] == '"' || text[pos] == '\'') {
				pos = skipHorizontal(text, pos+1)
			}
			if pos >= len(text) || (text[pos] != '=' && text[pos] != ':') {
				from = start + len(trigger)
				continue
			}
			pos = skipHorizontal(text, pos+1)
			quote := byte(0)
			escapedQuote := false
			switch {
			case pos < len(text) && (text[pos] == '"' || text[pos] == '\''):
				quote = text[pos]
				pos++
			case pos+1 < len(text) && text[pos] == '\\' && (text[pos+1] == '"' || text[pos+1] == '\''):
				// JSONL observations quote strings, so a password inside a
				// raw source record appears as \"value\".
				quote = text[pos+1]
				escapedQuote = true
				pos += 2
			}
			valueStart := pos
			if quote != 0 {
				for pos < len(text) {
					if escapedQuote {
						if text[pos] == '\\' && pos+1 < len(text) && text[pos+1] == quote {
							break
						}
					} else if text[pos] == quote {
						break
					}
					if text[pos] == '\n' || text[pos] == '\r' {
						break
					}
					pos++
				}
				closed := pos < len(text) && (escapedQuote && text[pos] == '\\' && pos+1 < len(text) && text[pos+1] == quote || !escapedQuote && text[pos] == quote)
				if !closed || pos == valueStart {
					from = start + len(trigger)
					continue
				}
			} else {
				for pos < len(text) && !isValueStop(text[pos]) {
					pos++
				}
			}
			valueEnd := pos
			value := text[valueStart:valueEnd]
			if !plausiblePassword(value, quote != 0) {
				from = start + len(trigger)
				continue
			}
			out = append(out, Finding{Start: valueStart, End: valueEnd, RuleID: "password-assignment", priority: 85})
			from = valueEnd
		}
	}
	return out
}

func skipHorizontal(text string, pos int) int {
	for pos < len(text) && (text[pos] == ' ' || text[pos] == '\t') {
		pos++
	}
	return pos
}

func isValueStop(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '"', '\'', ',', ';', ')', ']', '}':
		return true
	}
	return false
}

func plausiblePassword(value string, quoted bool) bool {
	v := strings.TrimSpace(value)
	if len(v) < 6 || len(v) > 256 || strings.ContainsAny(v, "\r\n") {
		return false
	}
	lower := strings.ToLower(v)
	switch lower {
	case "changeme", "change_me", "password", "passwd", "secret", "example",
		"placeholder", "configuration", "redacted", "none", "null", "undefined",
		"true", "false", "test", "testing", "dummy", "sample",
		"your_password", "your-password", "yourpassword":
		return false
	}
	for _, bad := range []string{"your_password", "change_me", "placeholder", "example_password", "redacted"} {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	for _, prefix := range []string{"$", "{{", "op://", "<", "os.", "env.", "getenv", "string(", "data.", "secretref", "vault:"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	if !quoted && strings.ContainsAny(v, "(){}<>$`\\") {
		return false
	}
	if strings.Trim(v, v[:1]) == "" {
		return false
	}
	var alnum bool
	for i := range len(v) {
		b := v[i]
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' {
			alnum = true
			break
		}
	}
	return alnum
}

// isTokenByte is the body alphabet shared by provider tokens. Dots are
// excluded so a token followed by sentence punctuation does not swallow it.
func isTokenByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-'
}

func isIdentByte(b byte) bool {
	return isTokenByte(b)
}

func plausibleToken(value string) bool {
	if value == "" || strings.Trim(value, value[:1]) == "" {
		return false
	}
	lower := strings.ToLower(value)
	for _, bad := range []string{
		"your", "example", "placeholder", "changeme", "change-me", "replace",
		"sample", "dummy", "redacted", "secret_here", "api_key_here", "xxxx",
	} {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	var letter, digit bool
	for i := range len(value) {
		switch b := value[i]; {
		case b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z':
			letter = true
		case b >= '0' && b <= '9':
			digit = true
		}
	}
	return letter && digit
}

// indexFoldASCII finds name in s, folding ASCII case only. Byte offsets are
// preserved because both sides are ASCII; provider variable names are ASCII.
func indexFoldASCII(s, name string) int {
	if name == "" || len(name) > len(s) {
		return -1
	}
	for i := range len(s) - len(name) + 1 {
		ok := true
		for j := range len(name) {
			if lowerASCII(s[i+j]) != lowerASCII(name[j]) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
