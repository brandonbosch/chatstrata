package redactions

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/brandonbosch/chatstrata/v2/internal/redact"
)

// jsonString is one string literal's content range in a JSON document. The
// surrounding quotes are excluded.
type jsonString struct {
	start int
	end   int
}

// jsonStrings returns every string literal in a JSON document. DuckDB's JSON
// column type guarantees valid JSON, so a literal always has a closing quote.
func jsonStrings(raw string) []jsonString {
	var out []jsonString
	for i := 0; i < len(raw); {
		if raw[i] != '"' {
			i++
			continue
		}
		start := i + 1
		j := start
		for ; j < len(raw); j++ {
			if raw[j] == '\\' {
				j++ // skip the escaped byte
				continue
			}
			if raw[j] == '"' {
				break
			}
		}
		if j >= len(raw) {
			break
		}
		out = append(out, jsonString{start: start, end: j})
		i = j + 1
	}
	return out
}

// scanJSONValue redacts one JSON document. Detection runs on each decoded
// string value, and the findings are mapped back to the serialized text, so a
// span can never begin or end inside an escape sequence. Replacing such a span
// with a typed placeholder therefore keeps the document valid JSON.
func scanJSONValue(engine *redact.Engine, scope redact.Scope, raw string) ([]redact.Span, error) {
	var spans []redact.Span
	for _, lit := range jsonStrings(raw) {
		content := raw[lit.start:lit.end]
		if content == "" {
			continue
		}
		decoded, err := decodeJSONString(content)
		if err != nil {
			return nil, err
		}
		if decoded == "" {
			continue
		}
		result, err := engine.RedactScope(scope, decoded)
		if err != nil {
			return nil, err
		}
		if len(result.Findings) == 0 {
			continue
		}
		escapes, err := jsonEscapes(content)
		if err != nil {
			return nil, err
		}
		for _, f := range result.Findings {
			start := escapes.rawOffset(f.Start, false)
			end := escapes.rawOffset(f.End, true)
			if end <= start {
				return nil, fmt.Errorf("map redaction span %d..%d in JSON string", f.Start, f.End)
			}
			span := f.Span()
			span.Start = lit.start + start
			span.End = lit.start + end
			spans = append(spans, span)
		}
	}
	return spans, nil
}

// jsonUnit is one decoded unit of a JSON string: either a literal byte or one
// escape sequence. dec holds the UTF-8 bytes the unit contributes.
type jsonUnit struct {
	rawLen int
	decLen int
	dec    [utf8.UTFMax]byte
}

func decodeJSONString(content string) (string, error) {
	var b strings.Builder
	b.Grow(len(content))
	for i := 0; i < len(content); {
		u, err := nextJSONUnit(content, i)
		if err != nil {
			return "", err
		}
		b.Write(u.dec[:u.decLen])
		i += u.rawLen
	}
	return b.String(), nil
}

func nextJSONUnit(s string, i int) (jsonUnit, error) {
	var u jsonUnit
	if s[i] != '\\' {
		u.rawLen, u.decLen = 1, 1
		u.dec[0] = s[i]
		return u, nil
	}
	if i+1 >= len(s) {
		return u, errors.New("truncated JSON escape")
	}
	u.rawLen = 2
	switch s[i+1] {
	case '"', '\\', '/':
		u.dec[0] = s[i+1]
	case 'b':
		u.dec[0] = '\b'
	case 'f':
		u.dec[0] = '\f'
	case 'n':
		u.dec[0] = '\n'
	case 'r':
		u.dec[0] = '\r'
	case 't':
		u.dec[0] = '\t'
	case 'u':
		r, n, err := decodeUnicodeEscape(s, i)
		if err != nil {
			return u, err
		}
		u.rawLen = n
		u.decLen = utf8.EncodeRune(u.dec[:], r)
		return u, nil
	default:
		return u, fmt.Errorf("invalid JSON escape %q", s[i:i+2])
	}
	u.decLen = 1
	return u, nil
}

// decodeUnicodeEscape decodes \uXXXX at s[i], folding a surrogate pair into
// one rune. A lone surrogate becomes U+FFFD, matching encoding/json.
func decodeUnicodeEscape(s string, i int) (rune, int, error) {
	if i+6 > len(s) {
		return 0, 0, errors.New("truncated JSON unicode escape")
	}
	first, err := parseHex4(s[i+2 : i+6])
	if err != nil {
		return 0, 0, err
	}
	r := rune(first)
	if !utf16.IsSurrogate(r) {
		return r, 6, nil
	}
	if i+12 <= len(s) && s[i+6] == '\\' && s[i+7] == 'u' {
		second, err := parseHex4(s[i+8 : i+12])
		if err == nil {
			if combined := utf16.DecodeRune(r, rune(second)); combined != utf8.RuneError {
				return combined, 12, nil
			}
		}
	}
	return utf8.RuneError, 6, nil
}

func parseHex4(s string) (uint16, error) {
	v, err := strconv.ParseUint(s, 16, 16)
	if err != nil {
		return 0, fmt.Errorf("invalid JSON unicode escape %q", s)
	}
	return uint16(v), nil
}

// jsonEscape is one escape sequence that changes the raw-to-decoded offset.
// delta is the cumulative shift after the sequence, so decoded offset d maps
// to raw offset d+delta when d is past every escape before it.
type jsonEscape struct {
	decStart int
	decLen   int
	rawStart int
	rawLen   int
	delta    int
}

type jsonEscapeMap []jsonEscape

func jsonEscapes(content string) (jsonEscapeMap, error) {
	var out jsonEscapeMap
	dec, raw := 0, 0
	for raw < len(content) {
		u, err := nextJSONUnit(content, raw)
		if err != nil {
			return nil, err
		}
		if u.rawLen != u.decLen {
			out = append(out, jsonEscape{
				decStart: dec,
				decLen:   u.decLen,
				rawStart: raw,
				rawLen:   u.rawLen,
				delta:    raw + u.rawLen - dec - u.decLen,
			})
		}
		dec += u.decLen
		raw += u.rawLen
	}
	return out, nil
}

// rawOffset maps a decoded byte offset back to the serialized string content.
// A boundary inside an escape sequence rounds down for a span start and up for
// a span end; either way the escape is replaced as a whole.
func (m jsonEscapeMap) rawOffset(d int, roundUp bool) int {
	i := sort.Search(len(m), func(j int) bool { return m[j].decStart+m[j].decLen > d })
	if i < len(m) && m[i].decStart < d {
		if roundUp {
			return m[i].rawStart + m[i].rawLen
		}
		return m[i].rawStart
	}
	shift := 0
	if i > 0 {
		shift = m[i-1].delta
	}
	return d + shift
}
