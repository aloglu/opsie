package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample(t *testing.T) (folder, vol string) {
	t.Helper()
	base := t.TempDir()
	folder, vol = filepath.Join(base, "app"), filepath.Join(base, "vol")
	for p, text := range map[string]string{
		"app/compose.yaml": "services: {}\n", "app/conf/site.conf": "a=1\n",
		"vol/db/data.bin": strings.Repeat("x", 100000),
	} {
		full := filepath.Join(base, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(folder, "conf/site.conf"), filepath.Join(folder, "conf/hard")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("conf/site.conf", filepath.Join(folder, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(folder, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "backups", "old.tar.gz"), []byte("skip me"), 0o600); err != nil {
		t.Fatal(err)
	}
	return folder, vol
}

func roundTrip(t *testing.T, recipient, identity string) {
	folder, vol := sample(t)
	m := Manifest{Format: Format, Project: "app", Created: time.Now().UTC().Truncate(time.Second), Folder: folder, Volumes: []string{"app_db"}}
	trees := []Tree{{Prefix: "folder", Path: folder, Skip: filepath.Join(folder, "backups")}, {Prefix: "volumes/app_db", Path: vol}}
	if n := Size(trees); n < 100000 || n > 100100 {
		t.Errorf("size %d", n)
	}
	var buf bytes.Buffer
	if err := Write(&buf, m, trees, recipient); err != nil {
		t.Fatal(err)
	}
	if recipient != "" && bytes.Contains(buf.Bytes(), []byte("compose.yaml")) {
		t.Fatal("encrypted backup shows its contents")
	}
	if _, c, err := Verify(bytes.NewReader(buf.Bytes()), identity); err != nil || c.Files != 3 || c.Bytes < 100000 {
		t.Errorf("verify: %+v %v", c, err)
	}
	// A flipped byte or a cut-off file is caught.
	bad := append([]byte(nil), buf.Bytes()...)
	bad[len(bad)/2] ^= 0xff
	if _, _, err := Verify(bytes.NewReader(bad), identity); err == nil {
		t.Error("a damaged backup passed")
	}
	if _, _, err := Verify(bytes.NewReader(buf.Bytes()[:len(buf.Bytes())-100]), identity); err == nil {
		t.Error("a cut-off backup passed")
	}
	got, err := ReadManifest(bytes.NewReader(buf.Bytes()), identity)
	if err != nil || got.Project != "app" {
		t.Fatalf("manifest %+v %v", got, err)
	}
	out := t.TempDir()
	_, err = Extract(bytes.NewReader(buf.Bytes()), identity, func(m Manifest) (map[string]string, error) {
		return map[string]string{"folder": filepath.Join(out, "f"), "volumes/app_db": filepath.Join(out, "v")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "f/conf/site.conf")); string(b) != "a=1\n" {
		t.Errorf("site.conf %q", b)
	}
	if info, err := os.Stat(filepath.Join(out, "f/conf/site.conf")); err != nil || info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v %v", info.Mode(), err)
	}
	a, _ := os.Stat(filepath.Join(out, "f/conf/site.conf"))
	h, _ := os.Stat(filepath.Join(out, "f/conf/hard"))
	if a == nil || h == nil || !os.SameFile(a, h) {
		t.Error("the hard link came back as a copy")
	}
	if l, _ := os.Readlink(filepath.Join(out, "f/link")); l != "conf/site.conf" {
		t.Errorf("link %q", l)
	}
	if _, err := os.Stat(filepath.Join(out, "f/backups")); err == nil {
		t.Error("the skipped folder was backed up")
	}
	if info, _ := os.Stat(filepath.Join(out, "v/db/data.bin")); info == nil || info.Size() != 100000 {
		t.Error("volume file missing")
	}
}

func TestRoundTrip(t *testing.T) { roundTrip(t, "", "") }

func TestEncrypted(t *testing.T) {
	secret, public, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if p := PublicKeys(secret); len(p) != 1 || p[0] != public {
		t.Fatal("PublicKeys doesn't match")
	}
	// A key copied off a narrow screen comes with line breaks in it.
	older, _, _ := NewKey()
	wrapped := "# created by opsie\n" + older + "\n# newer\n" + secret[:30] + "\n  " + secret[30:]
	roundTrip(t, public, wrapped+"\nIf it wraps, copy all of it.")
	if _, err := ParseIdentities(secret[:40]); err == nil {
		t.Error("half a key was accepted")
	}

	folder, _ := sample(t)
	var buf bytes.Buffer
	if err := Write(&buf, Manifest{Format: Format, Project: "app"}, []Tree{{Prefix: "folder", Path: folder}}, public); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(bytes.NewReader(buf.Bytes()), ""); !errors.Is(err, ErrNeedKey) {
		t.Errorf("no key: %v", err)
	}
	other, _, _ := NewKey()
	if _, err := ReadManifest(bytes.NewReader(buf.Bytes()), other); !errors.Is(err, ErrWrongKey) {
		t.Errorf("wrong key: %v", err)
	}
	if _, err := ParseRecipient("age1nope"); err == nil {
		t.Error("bad public key accepted")
	}
}

// hostile builds an archive by hand, the way a tampered one would look.
func hostile(t *testing.T, entries []tar.Header, bodies map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	data, _ := json.Marshal(Manifest{Format: Format, Project: "app"})
	tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg})
	tw.Write(data)
	for _, h := range entries {
		h.Size = int64(len(bodies[h.Name]))
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(bodies[h.Name]))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestNothingLandsOutside(t *testing.T) {
	outside := t.TempDir()
	for name, entries := range map[string][]tar.Header{
		"dot-dot": {{Name: "folder/../../escaped", Typeflag: tar.TypeReg, Mode: 0o644}},
		"through a link": {
			{Name: "folder/out", Typeflag: tar.TypeSymlink, Linkname: outside},
			{Name: "folder/out/escaped", Typeflag: tar.TypeReg, Mode: 0o644},
		},
		"unknown tree":  {{Name: "elsewhere/escaped", Typeflag: tar.TypeReg, Mode: 0o644}},
		"hard link out": {{Name: "folder/h", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}},
	} {
		data := hostile(t, entries, map[string]string{"folder/../../escaped": "x", "folder/out/escaped": "x", "elsewhere/escaped": "x"})
		dest := filepath.Join(t.TempDir(), "f")
		_, err := Extract(bytes.NewReader(data), "", func(Manifest) (map[string]string, error) {
			return map[string]string{"folder": dest}, nil
		})
		if err == nil {
			t.Errorf("%s: extracted without complaint", name)
		}
		if _, err := os.Stat(filepath.Join(outside, "escaped")); err == nil {
			t.Fatalf("%s: wrote outside", name)
		}
	}
}
