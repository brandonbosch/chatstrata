package scripts

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run the real POSIX installer with a local fake download client.
// No archive, account, release, or network service outside the test is touched.
func TestInstaller(t *testing.T) {
	for _, tc := range []struct {
		name, tags, version             string
		symlink, badChecksum, badBinary bool
	}{
		{"stable before newer candidate", `[ {"tag_name":"v2.1.0-rc.1"}, {"tag_name":"v0.5.0"}, {"tag_name":"v2.0.0"} ]`, "2.0.0", false, false, false},
		{"candidate fallback", `[ {"tag_name":"v0.5.0"}, {"tag_name":"v2.0.0-rc.1"} ]`, "2.0.0-rc.1", false, false, false},
		{"replace symlink", `[{"tag_name":"v2.0.0"}]`, "2.0.0", true, false, false},
		{"reject checksum", `[{"tag_name":"v2.0.0"}]`, "2.0.0", false, true, false},
		{"preserve working binary on incompatibility", `[{"tag_name":"v2.0.0"}]`, "2.0.0", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fakebin, dest := filepath.Join(root, "tools"), filepath.Join(root, "bin")
			for _, dir := range []string{fakebin, dest} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(name, body string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), mode); err != nil {
					t.Fatal(err)
				}
			}
			write("tools/uname", "#!/bin/sh\ncase $1 in -s) echo Linux;; -m) echo x86_64;; esac\n", 0o755)
			write("tools/curl", `#!/bin/sh
set -eu
out=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in -o) out=$2; shift 2;; -*) shift;; *) url=$1; shift;; esac
done
case "$url" in
  https://api.github.com/repos/brandonbosch/chatstrata/releases\?*) file="$INSTALL_FIXTURES/releases.json";;
  */checksums.txt) file="$INSTALL_FIXTURES/checksums.txt";;
  *.tar.gz) file="$INSTALL_FIXTURES/asset.tar.gz";;
  *) exit 22;;
esac
if [ -n "$out" ]; then cp "$file" "$out"; else cat "$file"; fi
`, 0o755)
			write("releases.json", tc.tags, 0o600)
			name := "chatstrata_" + tc.version + "_linux_amd64"
			body := "#!/bin/sh\n[ \"$1\" = version ] || exit 2\necho chatstrata " + tc.version + "\n"
			if tc.badBinary {
				body = "#!/bin/sh\nexit 127\n"
			}
			asset, err := os.Create(filepath.Join(root, "asset.tar.gz"))
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(asset)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&tar.Header{Name: name + "/chatstrata", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			if err := asset.Close(); err != nil {
				t.Fatal(err)
			}
			archive, err := os.ReadFile(filepath.Join(root, "asset.tar.gz"))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(archive)
			if tc.badChecksum {
				sum = sha256.Sum256([]byte("corrupt"))
			}
			write("checksums.txt", fmt.Sprintf("%x  %s.tar.gz\n", sum, name), 0o600)
			const original = "preserve this existing installation"
			write("old-python-entry", original, 0o755)
			target := filepath.Join(dest, "chatstrata")
			if tc.symlink {
				if err := os.Symlink(filepath.Join(root, "old-python-entry"), target); err != nil {
					t.Fatal(err)
				}
			} else {
				write("bin/chatstrata", original, 0o755)
			}
			cmd := exec.Command("sh", "install.sh")
			cmd.Env = append(os.Environ(), "PATH="+fakebin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "CHATSTRATA_VERSION=", "CHATSTRATA_INSTALL_DIR="+dest, "INSTALL_FIXTURES="+root)
			out, err := cmd.CombinedOutput()
			if tc.badChecksum || tc.badBinary {
				if err == nil {
					t.Fatalf("bad release installed: %s", out)
				}
				data, _ := os.ReadFile(target)
				if string(data) != original {
					t.Fatalf("existing install overwritten: %s", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("installer: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "Installed chatstrata "+tc.version) || !strings.Contains(string(out), "mcp config codex") {
				t.Fatalf("unexpected guidance: %s", out)
			}
			if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink != 0 {
				t.Fatal("target still a symlink")
			}
			old, _ := os.ReadFile(filepath.Join(root, "old-python-entry"))
			if string(old) != original {
				t.Fatal("installer wrote through Python symlink")
			}
		})
	}
}
