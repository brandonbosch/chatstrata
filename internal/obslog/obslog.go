// Package obslog is the observation log: the source of truth for an archive
// (ADR 0005).
//
// Each device appends immutable, numbered segments to its own directory:
//
//	<log>/identity.json                 this device's id and its space
//	<log>/devices/<device-id>/<seq>.seg  segments, named by their first sequence number
//
// A segment holds observations in sequence order. Each record is
//
//	uint32 header length | uint32 content length | uint32 CRC-32C | header JSON | zstd(content)
//
// with big-endian integers and the checksum over header and compressed content.
// Segments are written to a temporary file and renamed into place, so a reader
// never sees a partial one. Other devices' segments arrive in their own
// directories by sync (M3) and are read the same way.
package obslog

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// FormatVersion is written into every observation.
const FormatVersion = 1

type Kind string

const (
	// Append records bytes added at Offset of an append-only source file.
	Append Kind = "append"
	// Snapshot records the whole file.
	Snapshot Kind = "snapshot"
	// Tombstone records that the conversation was deleted from the archive.
	Tombstone Kind = "tombstone"
)

// Observation is what one device saw of one conversation's source file.
type Observation struct {
	FormatVersion int       `json:"v"`
	Space         string    `json:"space"`
	Device        string    `json:"device"`
	Seq           uint64    `json:"seq"`
	Source        string    `json:"source"`
	Scope         string    `json:"scope,omitempty"`
	Locator       string    `json:"locator"`
	Kind          Kind      `json:"kind"`
	Offset        int64     `json:"offset,omitempty"`
	Length        int64     `json:"length"`
	SHA256        string    `json:"sha256,omitempty"`
	ObservedAt    time.Time `json:"observed_at"`
	// Path is where the file lived on the observing device.
	Path string `json:"path,omitempty"`
	// ProjectHint is the adapter's fallback project for this file.
	ProjectHint string `json:"project_hint,omitempty"`
	// Legacy marks content imported from a Python-era archive: re-serialized
	// JSON rather than the original bytes.
	Legacy bool `json:"legacy,omitempty"`

	Content []byte `json:"-"`
}

// Key identifies the conversation an observation belongs to.
type Key struct{ Source, Scope, Locator string }

func (o *Observation) Key() Key { return Key{o.Source, o.Scope, o.Locator} }

// Location says where an observation's record sits, for reading its content
// back without scanning.
type Location struct {
	Segment  string // path relative to the log root
	Position int64  // byte offset of the record
}

// Identity names this device and the space it belongs to.
type Identity struct {
	Space  string `json:"space"`
	Device string `json:"device"`
}

// Log is an observation log rooted at a directory.
type Log struct {
	Root     string
	Identity Identity

	mu      sync.Mutex
	nextSeq uint64
}

// MaxSegmentBytes bounds the uncompressed content written to one segment.
const MaxSegmentBytes = 16 << 20

// Open opens the log at root, creating it and a new device identity if needed.
func Open(root string) (*Log, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	id, err := loadOrCreateIdentity(filepath.Join(root, "identity.json"))
	if err != nil {
		return nil, err
	}
	l := &Log{Root: root, Identity: id}
	last, err := l.lastSeq(id.Device)
	if err != nil {
		return nil, err
	}
	l.nextSeq = last + 1
	return l, nil
}

func loadOrCreateIdentity(path string) (Identity, error) {
	var id Identity
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &id); err != nil || id.Space == "" || id.Device == "" {
			return id, fmt.Errorf("read %s: not a valid identity file", path)
		}
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return id, fmt.Errorf("read identity: %w", err)
	}
	id = Identity{Space: randomID(), Device: randomID()}
	b, _ = json.MarshalIndent(id, "", "  ")
	if err := writeFileAtomic(path, append(b, '\n')); err != nil {
		return id, fmt.Errorf("write identity: %w", err)
	}
	return id, nil
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b)
}

// DeviceDir is the directory holding a device's segments.
func (l *Log) DeviceDir(device string) string {
	return filepath.Join(l.Root, "devices", device)
}

// Devices lists the devices that have segments in the log, sorted.
func (l *Log) Devices() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(l.Root, "devices"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	var devices []string
	for _, e := range entries {
		if e.IsDir() {
			devices = append(devices, e.Name())
		}
	}
	sort.Strings(devices)
	return devices, nil
}

// Segment is one segment file of a device.
type Segment struct {
	Path     string // relative to the log root
	FirstSeq uint64
}

// Segments lists a device's segments in sequence order.
func (l *Log) Segments(device string) ([]Segment, error) {
	entries, err := os.ReadDir(l.DeviceDir(device))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list segments of %s: %w", device, err)
	}
	var segs []Segment
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".seg")
		if !ok || e.IsDir() {
			continue
		}
		first, err := strconv.ParseUint(name, 10, 64)
		if err != nil {
			continue
		}
		segs = append(segs, Segment{Path: filepath.Join("devices", device, e.Name()), FirstSeq: first})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].FirstSeq < segs[j].FirstSeq })
	return segs, nil
}

// lastSeq returns the highest sequence number a device has written, or 0.
func (l *Log) lastSeq(device string) (uint64, error) {
	segs, err := l.Segments(device)
	if err != nil || len(segs) == 0 {
		return 0, err
	}
	var last uint64
	err = l.ReadSegment(segs[len(segs)-1].Path, false, func(o *Observation, _ Location) error {
		last = o.Seq
		return nil
	})
	return last, err
}

// Append assigns sequence numbers to observations and writes them as new
// segments of this device, returning where each landed. Content is hashed
// here. Nothing is visible until each segment is completely on disk.
func (l *Log) Append(obs []*Observation) ([]Location, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(obs) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(l.DeviceDir(l.Identity.Device), 0o700); err != nil {
		return nil, fmt.Errorf("create device directory: %w", err)
	}

	locations := make([]Location, 0, len(obs))
	for start := 0; start < len(obs); {
		end, size := start, 0
		for end < len(obs) && (end == start || size+len(obs[end].Content) <= MaxSegmentBytes) {
			size += len(obs[end].Content)
			end++
		}
		locs, err := l.writeSegment(obs[start:end])
		if err != nil {
			return nil, err
		}
		locations = append(locations, locs...)
		start = end
	}
	return locations, nil
}

// WriteSegment stores observations that another device made, as sync
// delivers them: they keep their device and sequence numbers and must all
// come from that device, in sequence order. Writing a segment that already
// exists replaces it with identical content.
func (l *Log) WriteSegment(device string, obs []*Observation) error {
	if len(obs) == 0 {
		return nil
	}
	for i, o := range obs {
		if o.Device != device {
			return fmt.Errorf("observation from %s in a segment for %s", o.Device, device)
		}
		if i > 0 && o.Seq <= obs[i-1].Seq {
			return fmt.Errorf("observations out of sequence order")
		}
	}
	if err := os.MkdirAll(l.DeviceDir(device), 0o700); err != nil {
		return fmt.Errorf("create device directory: %w", err)
	}
	rel := filepath.Join("devices", device, fmt.Sprintf("%016d.seg", obs[0].Seq))
	data, _, err := encodeSegment(rel, obs, l.Identity.Space)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(l.Root, rel), data)
}

var (
	encoderOnce sync.Once
	encoder     *zstd.Encoder
	decoderOnce sync.Once
	decoder     *zstd.Decoder
	crcTable    = crc32.MakeTable(crc32.Castagnoli)
)

func zstdEncoder() *zstd.Encoder {
	encoderOnce.Do(func() { encoder, _ = zstd.NewWriter(nil) })
	return encoder
}

func zstdDecoder() *zstd.Decoder {
	decoderOnce.Do(func() { decoder, _ = zstd.NewReader(nil) })
	return decoder
}

const segmentMagic = "CHATSTRATA-SEG1\n"

func (l *Log) writeSegment(obs []*Observation) ([]Location, error) {
	first := l.nextSeq
	rel := filepath.Join("devices", l.Identity.Device, fmt.Sprintf("%016d.seg", first))
	for i, o := range obs {
		o.Device = l.Identity.Device
		o.Seq = first + uint64(i)
	}
	data, locations, err := encodeSegment(rel, obs, l.Identity.Space)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(l.Root, rel), data); err != nil {
		return nil, fmt.Errorf("write segment: %w", err)
	}
	l.nextSeq = first + uint64(len(obs))
	return locations, nil
}

// encodeSegment serializes observations, which already carry their device and
// sequence numbers, filling in the derived fields.
func encodeSegment(rel string, obs []*Observation, space string) ([]byte, []Location, error) {
	var buf bytes.Buffer
	buf.WriteString(segmentMagic)
	locations := make([]Location, len(obs))
	for i, o := range obs {
		o.FormatVersion = FormatVersion
		o.Space = space
		o.Length = int64(len(o.Content))
		if o.Kind != Tombstone {
			sum := sha256.Sum256(o.Content)
			o.SHA256 = hex.EncodeToString(sum[:])
		}
		if o.ObservedAt.IsZero() {
			o.ObservedAt = time.Now().UTC()
		}
		header, err := json.Marshal(o)
		if err != nil {
			return nil, nil, fmt.Errorf("encode observation: %w", err)
		}
		compressed := zstdEncoder().EncodeAll(o.Content, nil)
		locations[i] = Location{Segment: rel, Position: int64(buf.Len())}

		var prefix [12]byte
		binary.BigEndian.PutUint32(prefix[0:4], uint32(len(header)))
		binary.BigEndian.PutUint32(prefix[4:8], uint32(len(compressed)))
		crc := crc32.Update(0, crcTable, header)
		crc = crc32.Update(crc, crcTable, compressed)
		binary.BigEndian.PutUint32(prefix[8:12], crc)
		buf.Write(prefix[:])
		buf.Write(header)
		buf.Write(compressed)
	}
	return buf.Bytes(), locations, nil
}

// ErrCorrupt reports a segment record that fails its checksum or is cut short.
var ErrCorrupt = errors.New("corrupt segment")

// ReadSegment calls fn for every observation in a segment, in order. Content
// is decompressed only when withContent is set.
func (l *Log) ReadSegment(rel string, withContent bool, fn func(*Observation, Location) error) error {
	f, err := os.Open(filepath.Join(l.Root, rel))
	if err != nil {
		return fmt.Errorf("open segment: %w", err)
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	magic := make([]byte, len(segmentMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != segmentMagic {
		return fmt.Errorf("%s: %w: bad header", rel, ErrCorrupt)
	}
	pos := int64(len(segmentMagic))
	for {
		o, size, err := readRecord(r, withContent)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s at %d: %w", rel, pos, err)
		}
		if err := fn(o, Location{Segment: rel, Position: pos}); err != nil {
			return err
		}
		pos += size
	}
}

// ReadAt returns the observation, with content, at a location.
func (l *Log) ReadAt(loc Location) (*Observation, error) {
	f, err := os.Open(filepath.Join(l.Root, loc.Segment))
	if err != nil {
		return nil, fmt.Errorf("open segment: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(loc.Position, io.SeekStart); err != nil {
		return nil, err
	}
	o, _, err := readRecord(bufio.NewReader(f), true)
	if err != nil {
		return nil, fmt.Errorf("%s at %d: %w", loc.Segment, loc.Position, err)
	}
	return o, nil
}

func readRecord(r io.Reader, withContent bool) (*Observation, int64, error) {
	var prefix [12]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		if err == io.EOF {
			return nil, 0, io.EOF
		}
		return nil, 0, ErrCorrupt
	}
	headerLen := binary.BigEndian.Uint32(prefix[0:4])
	contentLen := binary.BigEndian.Uint32(prefix[4:8])
	body := make([]byte, int(headerLen)+int(contentLen))
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, 0, ErrCorrupt
	}
	if crc32.Checksum(body, crcTable) != binary.BigEndian.Uint32(prefix[8:12]) {
		return nil, 0, ErrCorrupt
	}
	var o Observation
	if err := json.Unmarshal(body[:headerLen], &o); err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if o.FormatVersion > FormatVersion {
		return nil, 0, fmt.Errorf("observation format %d is newer than this version of chatstrata supports (%d)",
			o.FormatVersion, FormatVersion)
	}
	if withContent {
		content, err := zstdDecoder().DecodeAll(body[headerLen:], nil)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		o.Content = content
	}
	return &o, int64(len(prefix)) + int64(len(body)), nil
}

// writeFileAtomic writes data to path via a synced temporary file and rename.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
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
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
