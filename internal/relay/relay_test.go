package relay

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	space  = "0123456789abcdef0123456789abcdef"
	device = "fedcba9876543210fedcba9876543210"
	token  = "a-token-that-is-at-least-32-characters-long"
)

func do(t *testing.T, srv *httptest.Server, method, path, tok string, body []byte) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestRelayRoundTripAndAuth(t *testing.T) {
	srv := httptest.NewServer((&Server{Dir: t.TempDir()}).Handler())
	defer srv.Close()
	seg := "/v1/spaces/" + space + "/devices/" + device + "/segments"

	if code, _ := do(t, srv, "GET", "/v1/spaces/"+space+"/devices", token, nil); code != 404 {
		t.Errorf("unknown space: %d, want 404", code)
	}
	if code, _ := do(t, srv, "PUT", seg+"/1", token, []byte("ciphertext")); code != 201 {
		t.Fatalf("first upload: %d", code)
	}
	if code, _ := do(t, srv, "PUT", seg+"/1", token, []byte("other")); code != 409 {
		t.Errorf("re-upload: %d, want 409", code)
	}
	if code, _ := do(t, srv, "PUT", seg+"/5", "a-different-token-also-32-characters-long", []byte("x")); code != 403 {
		t.Errorf("wrong token: %d, want 403", code)
	}
	if code, _ := do(t, srv, "GET", seg+"/1", "", nil); code != 401 {
		t.Errorf("no token: %d, want 401", code)
	}
	if code, body := do(t, srv, "GET", seg+"/1", token, nil); code != 200 || body != "ciphertext" {
		t.Errorf("download: %d %q", code, body)
	}
	if code, body := do(t, srv, "GET", seg, token, nil); code != 200 || !strings.Contains(body, "[1]") {
		t.Errorf("list: %d %q", code, body)
	}
	if code, body := do(t, srv, "GET", "/v1/spaces/"+space+"/devices", token, nil); code != 200 || !strings.Contains(body, device) {
		t.Errorf("devices: %d %q", code, body)
	}
	if code, _ := do(t, srv, "GET", "/v1/spaces/../devices", token, nil); code != 400 && code != 404 {
		t.Errorf("path traversal: %d", code)
	}
	if code, _ := do(t, srv, "PUT", "/v1/spaces/"+space+"/devices/NOT-HEX/segments/1", token, []byte("x")); code != 400 {
		t.Errorf("bad device id: %d, want 400", code)
	}
}
