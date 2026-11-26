package redact

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// ConfigEnv names an explicit TOML policy file. When it is unset, LoadPolicy
// reads <config>/chatstrata/redaction.toml if that file exists.
const ConfigEnv = "CHATSTRATA_REDACTION_CONFIG"

// Scope selects the project and source path a conversation belongs to. User
// rules and dictionaries can restrict themselves to either.
type Scope struct {
	Project string
	Path    string
}

// UserRule is a user-defined regex detector. Pattern is Go RE2. SecretGroup 1
// selects the first capture group; 0 uses the whole match. Category names the
// placeholder; it defaults to the uppercase rule ID.
type UserRule struct {
	ID          string   `toml:"id" json:"id"`
	Pattern     string   `toml:"pattern" json:"pattern"`
	SecretGroup int      `toml:"secret_group" json:"secret_group"`
	Category    string   `toml:"category" json:"category"`
	Priority    int      `toml:"priority" json:"priority"`
	Entropy     float64  `toml:"entropy" json:"entropy"`
	Allowlist   []string `toml:"allowlist" json:"allowlist"`
	Projects    []string `toml:"projects" json:"projects"`
	Paths       []string `toml:"paths" json:"paths"`
}

// Dictionary is a list of literal terms, useful for names and project names
// that regexes cannot identify reliably. Terms are matched on word boundaries.
type Dictionary struct {
	ID            string   `toml:"id" json:"id"`
	Terms         []string `toml:"terms" json:"terms"`
	Category      string   `toml:"category" json:"category"`
	CaseSensitive bool     `toml:"case_sensitive" json:"case_sensitive"`
	Priority      int      `toml:"priority" json:"priority"`
	Projects      []string `toml:"projects" json:"projects"`
	Paths         []string `toml:"paths" json:"paths"`
}

type fileConfig struct {
	Redaction redactionFile `toml:"redaction"`
}

type redactionFile struct {
	Profile              string       `toml:"profile"`
	MaxInputBytes        int          `toml:"max_input_bytes"`
	Collect              bool         `toml:"collect"`
	Usernames            []string     `toml:"usernames"`
	PrivateHosts         []string     `toml:"private_hosts"`
	DisableLocalUsername bool         `toml:"disable_local_username"`
	Rules                []UserRule   `toml:"rules"`
	Dictionaries         []Dictionary `toml:"dictionaries"`
}

// DefaultConfigPath is <XDG_CONFIG_HOME>/chatstrata/redaction.toml, falling
// back to ~/.config. It is only read when the file exists.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	return filepath.Join(config, "chatstrata", "redaction.toml"), nil
}

// LoadPolicy loads a TOML policy. An explicit path (argument or
// CHATSTRATA_REDACTION_CONFIG) must exist. Without one, a missing default file
// means the built-in policy. Unknown keys, malformed TOML and invalid profiles
// are errors: a policy that silently ignored a typo would be unsafe.
func LoadPolicy(path string) (Policy, error) {
	explicit := path != ""
	if path == "" {
		path = os.Getenv(ConfigEnv)
		explicit = path != ""
	}
	if path == "" {
		defaultPath, err := DefaultConfigPath()
		if err != nil {
			return Policy{}, fmt.Errorf("resolve default redaction policy: %w", err)
		}
		path = defaultPath
	}
	var cfg fileConfig
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		if os.IsNotExist(err) && !explicit {
			return DefaultPolicy(), nil
		}
		return Policy{}, fmt.Errorf("read redaction policy %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return Policy{}, fmt.Errorf("redaction policy %s: unknown key %q", path, undecoded[0].String())
	}

	p := DefaultPolicy()
	if cfg.Redaction.Profile != "" {
		p.Profile, err = ParseProfile(cfg.Redaction.Profile)
		if err != nil {
			return Policy{}, fmt.Errorf("redaction policy %s: %w", path, err)
		}
	}
	if cfg.Redaction.MaxInputBytes > 0 {
		p.MaxInputBytes = cfg.Redaction.MaxInputBytes
	}
	p.Collect = cfg.Redaction.Collect
	p.Usernames = cfg.Redaction.Usernames
	p.PrivateHosts = cfg.Redaction.PrivateHosts
	p.DisableLocalUsername = cfg.Redaction.DisableLocalUsername
	p.Rules = cfg.Redaction.Rules
	p.Dictionaries = cfg.Redaction.Dictionaries
	return p, nil
}
