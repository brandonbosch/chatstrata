package devsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"filippo.io/age"

	"github.com/brandonbosch/chatstrata/internal/obslog"
)

// Client talks to a relay for one space.
type Client struct {
	Space *Space
	HTTP  *http.Client
}

func NewClient(s *Space) *Client {
	return &Client{Space: s, HTTP: &http.Client{Timeout: 2 * time.Minute}}
}

func (c *Client) url(path string) string {
	return c.Space.Relay + "/v1/spaces/" + c.Space.ID + path
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Space.Token())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("relay %s: %w", c.Space.Relay, err)
	}
	return resp, nil
}

func statusError(resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("relay answered %s: %s", resp.Status, bytes.TrimSpace(msg))
}

// RemoteDevice is a device the relay holds segments for.
type RemoteDevice struct {
	Device   string `json:"device"`
	Segments int    `json:"segments"`
	LastSeq  uint64 `json:"last_segment"`
}

// Devices lists the devices with segments on the relay. A space nobody has
// uploaded to yet has none.
func (c *Client) Devices(ctx context.Context) ([]RemoteDevice, error) {
	resp, err := c.do(ctx, "GET", "/devices", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}
	var out struct {
		Devices []RemoteDevice `json:"devices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("relay device list: %w", err)
	}
	return out.Devices, nil
}

func (c *Client) segments(ctx context.Context, device string) (map[uint64]bool, error) {
	resp, err := c.do(ctx, "GET", "/devices/"+device+"/segments", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return map[uint64]bool{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}
	var out struct {
		Segments []uint64 `json:"segments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("relay segment list: %w", err)
	}
	set := make(map[uint64]bool, len(out.Segments))
	for _, s := range out.Segments {
		set[s] = true
	}
	return set, nil
}

// Result counts what a sync moved.
type Result struct {
	Uploaded   int
	Downloaded int
}

// Sync uploads this device's segments the relay lacks and downloads other
// devices' segments this log lacks. Downloaded segments are validated and
// stored; applying them to the archive is the caller's catch-up.
//
// Each segment moves on its own, so an interrupted sync leaves only whole
// segments behind and the next run picks up where it stopped.
func (c *Client) Sync(ctx context.Context, l *obslog.Log) (Result, error) {
	var res Result
	recipient := c.Space.Key.Recipient()

	// Push.
	self := l.Identity.Device
	local, err := l.Segments(self)
	if err != nil {
		return res, err
	}
	if len(local) > 0 {
		remote, err := c.segments(ctx, self)
		if err != nil {
			return res, err
		}
		for _, seg := range local {
			if remote[seg.FirstSeq] {
				continue
			}
			plain, err := l.SegmentBytes(seg.Path)
			if err != nil {
				return res, err
			}
			var buf bytes.Buffer
			w, err := age.Encrypt(&buf, recipient)
			if err != nil {
				return res, err
			}
			if _, err := w.Write(plain); err != nil {
				return res, err
			}
			if err := w.Close(); err != nil {
				return res, err
			}
			resp, err := c.do(ctx, "PUT", "/devices/"+self+"/segments/"+strconv.FormatUint(seg.FirstSeq, 10), buf.Bytes())
			if err != nil {
				return res, err
			}
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusCreated:
				res.Uploaded++
			case http.StatusConflict: // uploaded by an earlier, interrupted run
			default:
				return res, statusError(resp)
			}
		}
	}

	// Pull.
	devices, err := c.Devices(ctx)
	if err != nil {
		return res, err
	}
	for _, d := range devices {
		if d.Device == self {
			continue
		}
		have := map[uint64]bool{}
		segs, err := l.Segments(d.Device)
		if err != nil {
			return res, err
		}
		for _, s := range segs {
			have[s.FirstSeq] = true
		}
		remote, err := c.segments(ctx, d.Device)
		if err != nil {
			return res, err
		}
		for _, seq := range sortedKeys(remote) {
			if have[seq] {
				continue
			}
			if err := c.download(ctx, l, d.Device, seq); err != nil {
				return res, err
			}
			res.Downloaded++
		}
	}
	return res, nil
}

func (c *Client) download(ctx context.Context, l *obslog.Log, device string, seq uint64) error {
	resp, err := c.do(ctx, "GET", "/devices/"+device+"/segments/"+strconv.FormatUint(seq, 10), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	r, err := age.Decrypt(resp.Body, c.Space.Key)
	if err != nil {
		return fmt.Errorf("segment %d of device %s: can't decrypt (wrong space key, or not written by a member): %w", seq, device, err)
	}
	plain, err := io.ReadAll(io.LimitReader(r, 2*obslog.MaxSegmentBytes+1<<20))
	if err != nil {
		return fmt.Errorf("segment %d of device %s: %w", seq, device, err)
	}
	if err := l.ImportSegment(device, seq, plain); err != nil {
		return fmt.Errorf("segment %d of device %s: %w", seq, device, err)
	}
	return nil
}

func sortedKeys(m map[uint64]bool) []uint64 {
	out := make([]uint64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
