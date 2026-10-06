// Package devsync moves observation-log segments between devices through a
// relay (ADR 0005). Segments are encrypted with the space key before they
// leave the device; the relay only ever stores ciphertext.
//
// Per log, next to identity.json:
//
//	space.key   the space's age identity (mode 0600); never printed except by `pair`
//	sync.json   the relay URL
package devsync

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// Space is what a device needs to sync: the space id, its key and the relay.
type Space struct {
	ID    string
	Key   *age.X25519Identity
	Relay string
}

// Token is the relay credential for the space, derived from the key so every
// device that holds the key has it and nothing else needs distributing.
func (s *Space) Token() string {
	mac := hmac.New(sha256.New, []byte(s.Key.String()))
	mac.Write([]byte("chatstrata relay token v1\x00" + s.ID))
	return hex.EncodeToString(mac.Sum(nil))
}

type config struct {
	Relay string `json:"relay"`
}

var ErrNotPaired = errors.New("this device isn't set up to sync; run `chatstrata pair --relay URL` on your first device and `chatstrata join CODE` on the others")

// Load reads the space key and relay of the log at root.
func Load(root, spaceID string) (*Space, error) {
	keyBytes, err := os.ReadFile(filepath.Join(root, "space.key"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotPaired
	}
	if err != nil {
		return nil, fmt.Errorf("read space key: %w", err)
	}
	key, err := age.ParseX25519Identity(strings.TrimSpace(string(keyBytes)))
	if err != nil {
		return nil, errors.New("space.key is not a valid key")
	}
	var cfg config
	if b, err := os.ReadFile(filepath.Join(root, "sync.json")); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("read sync.json: %w", err)
		}
	}
	if cfg.Relay == "" {
		return nil, ErrNotPaired
	}
	return &Space{ID: spaceID, Key: key, Relay: cfg.Relay}, nil
}

// Save writes the space key and relay for the log at root.
func Save(root string, s *Space) error {
	if err := writePrivate(filepath.Join(root, "space.key"), []byte(s.Key.String()+"\n")); err != nil {
		return fmt.Errorf("write space key: %w", err)
	}
	b, _ := json.MarshalIndent(config{Relay: s.Relay}, "", "  ")
	if err := writePrivate(filepath.Join(root, "sync.json"), append(b, '\n')); err != nil {
		return fmt.Errorf("write sync.json: %w", err)
	}
	return nil
}

// NewKey generates a space key.
func NewKey() (*age.X25519Identity, error) { return age.GenerateX25519Identity() }

// ValidateRelay checks a relay URL.
func ValidateRelay(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("relay must be an http(s) URL, got %q", raw)
	}
	return u.String(), nil
}

const codePrefix = "chatstrata-join:"

type code struct {
	Space string `json:"s"`
	Key   string `json:"k"`
	Relay string `json:"r"`
}

// PairingCode encodes everything another device needs to join the space.
// It contains the key: treat it like a password.
func (s *Space) PairingCode() string {
	b, _ := json.Marshal(code{Space: s.ID, Key: s.Key.String(), Relay: s.Relay})
	return codePrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ParsePairingCode decodes a code made by PairingCode.
func ParsePairingCode(raw string) (*Space, error) {
	payload, ok := strings.CutPrefix(strings.TrimSpace(raw), codePrefix)
	if !ok {
		return nil, errors.New("not a chatstrata pairing code")
	}
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, errors.New("pairing code is damaged")
	}
	var c code
	if err := json.Unmarshal(b, &c); err != nil || len(c.Space) != 32 {
		return nil, errors.New("pairing code is damaged")
	}
	key, err := age.ParseX25519Identity(c.Key)
	if err != nil {
		return nil, errors.New("pairing code is damaged")
	}
	relay, err := ValidateRelay(c.Relay)
	if err != nil {
		return nil, err
	}
	return &Space{ID: c.Space, Key: key, Relay: relay}, nil
}

func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
