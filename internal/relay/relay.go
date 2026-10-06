// Package relay is the store-and-forward service devices sync through
// (ADR 0005). It keeps encrypted segments per space and device and does
// nothing else: it can't read them, merge them or change them.
//
// API, all under /v1/spaces/{space}:
//
//	GET  /devices                              devices and their segment counts
//	GET  /devices/{device}/segments            sequence numbers of a device's segments
//	GET  /devices/{device}/segments/{seq}      download one segment
//	PUT  /devices/{device}/segments/{seq}      upload one; 409 if it already exists
//
// Every request carries "Authorization: Bearer <token>". The token is derived
// from the space key, so only devices that hold the key have it. The first
// upload to a space records the token's hash; after that the space only
// answers to that token. The relay is plain HTTP: put it behind HTTPS, such as
// `tailscale serve` on a tailnet.
package relay

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// MaxSegmentBytes bounds one upload. Segments hold at most 16 MiB of
// uncompressed content, so this leaves ample room.
const MaxSegmentBytes = 64 << 20

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Server serves the relay API from a data directory.
type Server struct {
	Dir string
	Log *log.Logger

	mu sync.Mutex // serializes token registration
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/spaces/{space}/devices", s.listDevices)
	mux.HandleFunc("GET /v1/spaces/{space}/devices/{device}/segments", s.listSegments)
	mux.HandleFunc("GET /v1/spaces/{space}/devices/{device}/segments/{seq}", s.getSegment)
	mux.HandleFunc("PUT /v1/spaces/{space}/devices/{device}/segments/{seq}", s.putSegment)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	return mux
}

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log.Printf(format, args...)
	}
}

func (s *Server) spaceDir(space string) string { return filepath.Join(s.Dir, "spaces", space) }

func (s *Server) segmentPath(space, device string, seq uint64) string {
	return filepath.Join(s.spaceDir(space), "devices", device, fmt.Sprintf("%016d.seg.age", seq))
}

// params validates the path parameters present on the request.
func params(r *http.Request) (space, device string, seq uint64, err error) {
	space = r.PathValue("space")
	if !idPattern.MatchString(space) {
		return "", "", 0, errors.New("bad space id")
	}
	if device = r.PathValue("device"); device != "" && !idPattern.MatchString(device) {
		return "", "", 0, errors.New("bad device id")
	}
	if raw := r.PathValue("seq"); raw != "" {
		if seq, err = strconv.ParseUint(raw, 10, 64); err != nil || seq == 0 {
			return "", "", 0, errors.New("bad sequence number")
		}
	}
	return space, device, seq, nil
}

func bearer(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return token
}

func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return []byte(hex.EncodeToString(sum[:]))
}

// authorize checks the request's token against the space. With register set
// (uploads), an unknown space is created with this token.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, space string, register bool) bool {
	token := bearer(r)
	if len(token) < 32 {
		http.Error(w, "missing or malformed token", http.StatusUnauthorized)
		return false
	}
	path := filepath.Join(s.spaceDir(space), "token.sha256")
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if !register {
			http.Error(w, "no such space", http.StatusNotFound)
			return false
		}
		if err := os.MkdirAll(s.spaceDir(space), 0o700); err != nil {
			s.fail(w, err)
			return false
		}
		if err := os.WriteFile(path, tokenHash(token), 0o600); err != nil {
			s.fail(w, err)
			return false
		}
		s.logf("registered space %s", space)
		return true
	}
	if err != nil {
		s.fail(w, err)
		return false
	}
	if subtle.ConstantTimeCompare(stored, tokenHash(token)) != 1 {
		http.Error(w, "wrong token for this space", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.logf("error: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *Server) seqs(space, device string) ([]uint64, error) {
	entries, err := os.ReadDir(filepath.Join(s.spaceDir(space), "devices", device))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []uint64
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".seg.age")
		if !ok {
			continue
		}
		if n, err := strconv.ParseUint(name, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// DeviceInfo describes one device's segments on the relay.
type DeviceInfo struct {
	Device   string `json:"device"`
	Segments int    `json:"segments"`
	LastSeq  uint64 `json:"last_segment"`
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	space, _, _, err := params(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorize(w, r, space, false) {
		return
	}
	entries, err := os.ReadDir(filepath.Join(s.spaceDir(space), "devices"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.fail(w, err)
		return
	}
	devices := []DeviceInfo{}
	for _, e := range entries {
		if !e.IsDir() || !idPattern.MatchString(e.Name()) {
			continue
		}
		seqs, err := s.seqs(space, e.Name())
		if err != nil {
			s.fail(w, err)
			return
		}
		info := DeviceInfo{Device: e.Name(), Segments: len(seqs)}
		if len(seqs) > 0 {
			info.LastSeq = seqs[len(seqs)-1]
		}
		devices = append(devices, info)
	}
	writeJSON(w, map[string]any{"devices": devices})
}

func (s *Server) listSegments(w http.ResponseWriter, r *http.Request) {
	space, device, _, err := params(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorize(w, r, space, false) {
		return
	}
	seqs, err := s.seqs(space, device)
	if err != nil {
		s.fail(w, err)
		return
	}
	if seqs == nil {
		seqs = []uint64{}
	}
	writeJSON(w, map[string]any{"segments": seqs})
}

func (s *Server) getSegment(w http.ResponseWriter, r *http.Request) {
	space, device, seq, err := params(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorize(w, r, space, false) {
		return
	}
	f, err := os.Open(s.segmentPath(space, device, seq))
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, f)
}

func (s *Server) putSegment(w http.ResponseWriter, r *http.Request) {
	space, device, seq, err := params(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorize(w, r, space, true) {
		return
	}
	path := s.segmentPath(space, device, seq)
	if _, err := os.Stat(path); err == nil {
		http.Error(w, "segment already exists", http.StatusConflict)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		s.fail(w, err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		s.fail(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, http.MaxBytesReader(w, r.Body, MaxSegmentBytes))
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "segment too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "upload interrupted", http.StatusBadRequest)
		return
	}
	// Link rather than rename, so a concurrent upload of the same segment
	// can't replace one that already landed.
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			http.Error(w, "segment already exists", http.StatusConflict)
			return
		}
		s.fail(w, err)
		return
	}
	s.logf("stored %s/%s/%d (%d bytes)", space[:8], device[:8], seq, n)
	w.WriteHeader(http.StatusCreated)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
