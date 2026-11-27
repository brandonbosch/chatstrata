package label

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed packs/*.toml
var builtinPackFS embed.FS

// StateField describes how one target_sql column maps into the classifier state.
type StateField struct {
	Column  string `toml:"column" json:"column"`
	Clip    int    `toml:"clip" json:"clip,omitempty"`
	JSON    bool   `toml:"json" json:"json,omitempty"`
	Unwrap  bool   `toml:"unwrap" json:"unwrap,omitempty"`
	Default string `toml:"default" json:"default,omitempty"`
}

// QuestionSpec is one TypeSafe System One question in a pack.
type QuestionSpec struct {
	Type         string `toml:"type" json:"type"`
	Instructions string `toml:"instructions" json:"instructions"`
	Criteria     any    `toml:"criteria" json:"criteria,omitempty"`
}

// Pack is a declarative classifier question pack loaded from TOML.
type Pack struct {
	Name         string                  `toml:"name" json:"name"`
	Description  string                  `toml:"description" json:"description"`
	TargetKind   string                  `toml:"target_kind" json:"target_kind"`
	StateVersion int                     `toml:"state_version" json:"state_version"`
	DependsOn    []string                `toml:"depends_on" json:"depends_on,omitempty"`
	TargetSQL    string                  `toml:"target_sql" json:"target_sql"`
	State        map[string]StateField   `toml:"state" json:"state"`
	Questions    map[string]QuestionSpec `toml:"questions" json:"questions"`

	Source  string `toml:"-" json:"source,omitempty"`
	version string
}

// Version returns the stable 12-hex-char SHA-256 fingerprint of the pack's
// questions, state specification, and state_version.
func (p *Pack) Version() string {
	if p.version != "" {
		return p.version
	}
	payload := struct {
		Questions    map[string]QuestionSpec `json:"questions"`
		State        map[string]StateField   `json:"state"`
		StateVersion int                     `json:"state_version"`
	}{
		Questions:    p.Questions,
		State:        p.State,
		StateVersion: p.StateVersion,
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	p.version = hex.EncodeToString(sum[:])[:12]
	return p.version
}

// DefaultPacksDir returns the user pack discovery directory:
// $CHATSTRATA_PACKS_DIR, else <XDG_CONFIG_HOME>/chatstrata/packs.
func DefaultPacksDir() string {
	if p := os.Getenv("CHATSTRATA_PACKS_DIR"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "chatstrata", "packs")
}

// LoadPacks loads embedded built-in packs and overlays any *.toml files found
// in userDir (or DefaultPacksDir when userDir is empty). A missing default
// directory is ignored; an explicit missing userDir returns an error.
func LoadPacks(userDir string) (map[string]*Pack, error) {
	packs := map[string]*Pack{}
	entries, err := fs.ReadDir(builtinPackFS, "packs")
	if err != nil {
		return nil, fmt.Errorf("read built-in packs: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		data, err := builtinPackFS.ReadFile("packs/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read built-in pack %s: %w", e.Name(), err)
		}
		p, err := ParsePack(data, "builtin:"+e.Name())
		if err != nil {
			return nil, err
		}
		packs[p.Name] = p
	}

	explicit := userDir != ""
	if !explicit {
		userDir = DefaultPacksDir()
	}
	if userDir == "" {
		return packs, nil
	}
	dirEntries, err := os.ReadDir(userDir)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return packs, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read packs dir %s: %w", userDir, err)
	}
	for _, e := range dirEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		full := filepath.Join(userDir, e.Name())
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("read pack %s: %w", full, err)
		}
		p, err := ParsePack(data, full)
		if err != nil {
			return nil, err
		}
		packs[p.Name] = p
	}
	return packs, nil
}

type rawQuestionSpec struct {
	Type         string         `toml:"type"`
	Instructions string         `toml:"instructions"`
	Criteria     toml.Primitive `toml:"criteria"`
	HasCriteria  bool           `toml:"-"`
}

type rawPack struct {
	Name         string                     `toml:"name"`
	Description  string                     `toml:"description"`
	TargetKind   string                     `toml:"target_kind"`
	StateVersion int                        `toml:"state_version"`
	DependsOn    []string                   `toml:"depends_on"`
	TargetSQL    string                     `toml:"target_sql"`
	State        map[string]StateField      `toml:"state"`
	Questions    map[string]rawQuestionSpec `toml:"questions"`
}

// ParsePack decodes and validates one TOML pack definition.
func ParsePack(data []byte, source string) (*Pack, error) {
	var raw rawPack
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, fmt.Errorf("parse pack %s: %w", source, err)
	}
	p := Pack{
		Name:         strings.TrimSpace(raw.Name),
		Description:  strings.TrimSpace(raw.Description),
		TargetKind:   raw.TargetKind,
		StateVersion: raw.StateVersion,
		DependsOn:    raw.DependsOn,
		TargetSQL:    strings.TrimSpace(raw.TargetSQL),
		State:        raw.State,
		Questions:    make(map[string]QuestionSpec, len(raw.Questions)),
		Source:       source,
	}
	for qid, rq := range raw.Questions {
		var crit any
		if md.IsDefined("questions", qid, "criteria") {
			var err error
			crit, err = decodeCriteria(md, qid, rq.Type, rq.Criteria)
			if err != nil {
				return nil, fmt.Errorf("invalid pack %s: %w", source, err)
			}
		}
		p.Questions[qid] = QuestionSpec{
			Type:         rq.Type,
			Instructions: strings.TrimSpace(rq.Instructions),
			Criteria:     crit,
		}
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("parse pack %s: unknown keys %s", source, strings.Join(keys, ", "))
	}
	if p.StateVersion == 0 {
		p.StateVersion = 1
	}
	if err := validatePack(&p); err != nil {
		return nil, fmt.Errorf("invalid pack %s: %w", source, err)
	}
	_ = p.Version()
	return &p, nil
}

func decodeCriteria(md toml.MetaData, qid, kind string, prim toml.Primitive) (any, error) {
	switch kind {
	case "noul", "choice":
		var m map[string]string
		if err := md.PrimitiveDecode(prim, &m); err != nil {
			return nil, fmt.Errorf("question %q (%s) criteria must be a table of strings: %w", qid, kind, err)
		}
		for k, v := range m {
			m[k] = strings.TrimSpace(v)
		}
		return m, nil
	case "score":
		var s []string
		if err := md.PrimitiveDecode(prim, &s); err != nil {
			return nil, fmt.Errorf("question %q (score) criteria must be an array of strings: %w", qid, err)
		}
		for i, v := range s {
			s[i] = strings.TrimSpace(v)
		}
		return s, nil
	default:
		return nil, fmt.Errorf("question %q has unsupported type %q (want noul, choice, or score)", qid, kind)
	}
}

func validatePack(p *Pack) error {
	if p.Name == "" {
		return errors.New("missing name")
	}
	if p.Description == "" {
		return errors.New("missing description")
	}
	if !validTargetKind(p.TargetKind) {
		return fmt.Errorf("target_kind must be conversation, message, or tool_call (got %q)", p.TargetKind)
	}
	if p.TargetSQL == "" {
		return errors.New("missing target_sql")
	}
	if len(p.State) == 0 {
		return errors.New("pack must define at least one [state] field")
	}
	for name, sf := range p.State {
		if strings.TrimSpace(sf.Column) == "" {
			return fmt.Errorf("state field %q missing column", name)
		}
	}
	if len(p.Questions) == 0 {
		return errors.New("pack must define at least one question")
	}
	for qid, q := range p.Questions {
		if q.Instructions == "" {
			return fmt.Errorf("question %q missing instructions", qid)
		}
		switch q.Type {
		case "noul":
		case "choice":
			m, ok := q.Criteria.(map[string]string)
			if !ok || len(m) < 2 {
				return fmt.Errorf("question %q (choice) requires criteria table with at least 2 options", qid)
			}
		case "score":
			s, ok := q.Criteria.([]string)
			if !ok || len(s) < 2 {
				return fmt.Errorf("question %q (score) requires criteria array with at least 2 levels", qid)
			}
		default:
			return fmt.Errorf("question %q has unsupported type %q (want noul, choice, or score)", qid, q.Type)
		}
	}
	return nil
}

// TopologicalOrder orders packs so every pack appears after its depends_on
// prerequisites. Ties are broken by pack name.
func TopologicalOrder(packs map[string]*Pack) ([]*Pack, error) {
	names := make([]string, 0, len(packs))
	for name := range packs {
		names = append(names, name)
	}
	sort.Strings(names)

	state := map[string]int{} // 0=unvisited, 1=visiting, 2=done
	var order []*Pack
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("cyclic pack dependency involving %q", name)
		case 2:
			return nil
		}
		p, ok := packs[name]
		if !ok {
			return fmt.Errorf("unknown pack dependency %q", name)
		}
		state[name] = 1
		deps := append([]string(nil), p.DependsOn...)
		sort.Strings(deps)
		for _, dep := range deps {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = 2
		order = append(order, p)
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}
