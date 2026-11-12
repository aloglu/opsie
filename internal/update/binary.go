package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// AssetName is the release archive for an architecture. It has no version
// in it, so releases/latest/download/<name> always works.
func AssetName(goarch string) string {
	return "opsie_linux_" + goarch + ".tar.gz"
}

// InstallRelease downloads tag's archive, checks it against the release's
// checksums, and replaces target with the binary inside.
func InstallRelease(ctx context.Context, tag, target string) error {
	if runtime.GOOS != "linux" {
		return errors.New("opsie releases are built for Linux only")
	}
	name := AssetName(runtime.GOARCH)
	sums, err := fetch(ctx, tag, "checksums.txt")
	if err != nil {
		return err
	}
	want, err := checksumFor(sums, name)
	if err != nil {
		return err
	}
	archive, err := fetch(ctx, tag, name)
	if err != nil {
		return err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return errors.New("the download doesn't match its checksum, so it wasn't installed; please try again")
	}
	binary, err := extractBinary(archive)
	if err != nil {
		return err
	}
	return replaceFile(target, binary)
}

func fetch(ctx context.Context, tag, name string) ([]byte, error) {
	url := DownloadBase + "/download/" + tag + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "opsie-updater")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("couldn't download %s from %s (%s)", name, tag, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

func checksumFor(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("the release has no checksum for %s", name)
}

func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("the download isn't a valid archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the archive doesn't contain opsie")
		}
		if err != nil {
			return nil, fmt.Errorf("the download isn't a valid archive: %w", err)
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == "opsie" {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}

// replaceFile writes data next to target and renames it over, so the swap
// is atomic and a running opsie keeps its old copy until it exits.
func replaceFile(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".opsie-update-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("opsie can't write to %s; update it the way you installed it there", dir)
		}
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// Executable is the running binary's real path.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}
