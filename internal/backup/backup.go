// Package backup writes and reads opsie's app backups: one tar.gz holding
// an app's compose folder and its Docker volumes, optionally encrypted
// with age (https://age-encryption.org).
package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
)

// Format is the version of the archive layout.
const Format = 1

// manifestName is the archive's first entry.
const manifestName = "opsie-backup.json"

// Manifest describes a backup. It's the first thing in the archive, and a
// copy sits next to it so backups can be listed without opening them.
type Manifest struct {
	Format  int       `json:"format"`
	Project string    `json:"project"`
	Created time.Time `json:"created"`
	Host    string    `json:"host,omitempty"`
	Opsie   string    `json:"opsie,omitempty"`
	// Folder is where the compose folder was, and goes back to.
	Folder  string   `json:"folder"`
	Volumes []string `json:"volumes"`
	// Bytes is roughly how much the files take up unpacked.
	Bytes uint64 `json:"bytes"`
	// Stopped is whether the app was stopped while it was copied.
	Stopped bool `json:"stopped"`
	// Recipient is the public key it's encrypted to, or "".
	Recipient string `json:"recipient,omitempty"`
	// Skipped are mounted folders outside the compose folder, which
	// aren't in the backup.
	Skipped []string `json:"skipped,omitempty"`
}

// Tree is a folder that goes into the archive under Prefix.
type Tree struct {
	Prefix string // "folder" or "volumes/NAME"
	Path   string
	// Skip is a path inside it to leave out, like a backup folder that
	// sits inside the app's folder.
	Skip string
}

// Size adds up the files in trees.
func Size(trees []Tree) uint64 {
	var n uint64
	for _, t := range trees {
		_ = filepath.WalkDir(t.Path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if t.Skip != "" && p == t.Skip {
				return filepath.SkipDir
			}
			if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
				n += uint64(info.Size())
			}
			return nil
		})
	}
	return n
}

// Write writes the archive to w, encrypting it to recipient when it isn't
// "".
func Write(w io.Writer, m Manifest, trees []Tree, recipient string) error {
	var out io.Writer = w
	var enc io.WriteCloser
	if recipient != "" {
		r, err := ParseRecipient(recipient)
		if err != nil {
			return err
		}
		if enc, err = age.Encrypt(w, r); err != nil {
			return err
		}
		out = enc
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(data)), ModTime: m.Created, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	for _, t := range trees {
		if err := addTree(tw, t); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if enc != nil {
		return enc.Close()
	}
	return nil
}

func addTree(tw *tar.Writer, t Tree) error {
	// Files with several names are stored once; the other names are links
	// to the first, as they were.
	type inode struct{ dev, ino uint64 }
	seen := map[inode]string{}
	return filepath.WalkDir(t.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == t.Path {
				return err
			}
			if errors.Is(err, fs.ErrNotExist) {
				return nil // removed while we read
			}
			return err
		}
		if t.Skip != "" && p == t.Skip {
			return filepath.SkipDir
		}
		info, err := os.Lstat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(t.Path, p)
		if err != nil {
			return err
		}
		name := t.Prefix
		if rel != "." {
			name += "/" + filepath.ToSlash(rel)
		}
		link := ""
		switch mode := info.Mode(); {
		case mode&fs.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		case mode.IsDir(), mode.IsRegular():
		default:
			return nil // sockets, pipes and devices don't belong in a backup
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name, hdr.Uname, hdr.Gname, hdr.Format = name, "", "", tar.FormatPAX
		if info.IsDir() {
			hdr.Name += "/"
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			hdr.Uid, hdr.Gid = int(st.Uid), int(st.Gid)
			if info.Mode().IsRegular() && st.Nlink > 1 {
				key := inode{uint64(st.Dev), st.Ino}
				if first, ok := seen[key]; ok {
					hdr.Typeflag, hdr.Linkname, hdr.Size = tar.TypeLink, first, 0
					return tw.WriteHeader(hdr)
				}
				seen[key] = name
			}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		// A file that changes while it's read still takes exactly the size
		// its header says.
		n, err := io.CopyN(tw, f, hdr.Size)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n < hdr.Size {
			_, err = io.CopyN(tw, zeros{}, hdr.Size-n)
		}
		return err
	})
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// ErrWrongKey is returned when the key given doesn't open a backup.
var ErrWrongKey = errors.New("this key doesn't open this backup; it was encrypted to a different one")

// ErrNeedKey is returned for an encrypted backup when no key was given.
var ErrNeedKey = errors.New("this backup is encrypted; its secret key is needed to restore it")

// Extract unpacks an archive. Once the manifest is read, place says which
// folder each tree goes into ("folder", "volumes/NAME"); those folders must
// not exist yet. Nothing outside them is written to, whatever the archive
// holds.
func Extract(r io.Reader, identity string, place func(Manifest) (map[string]string, error)) (Manifest, error) {
	var m Manifest
	in, err := decrypt(r, identity)
	if err != nil {
		return m, err
	}
	gz, err := gzip.NewReader(in)
	if err != nil {
		return m, fmt.Errorf("this isn't an opsie backup, or it's damaged: %w", err)
	}
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil || hdr.Name != manifestName {
		return m, errors.New("this isn't an opsie backup: it has no description of what's in it")
	}
	if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m); err != nil {
		return m, fmt.Errorf("this backup's description is damaged: %w", err)
	}
	if m.Format != Format {
		return m, fmt.Errorf("this backup is in format %d, which this opsie doesn't know; update opsie", m.Format)
	}
	dirs, err := place(m)
	if err != nil {
		return m, err
	}
	roots := map[string]*os.Root{}
	defer func() {
		for _, r := range roots {
			r.Close()
		}
	}()
	for prefix, dir := range dirs {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return m, err
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return m, err
		}
		roots[prefix] = root
	}
	type later struct {
		root *os.Root
		name string
		hdr  *tar.Header
	}
	var dirsLater []later
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, fmt.Errorf("this backup is damaged or cut short: %w", err)
		}
		prefix, name, err := split(hdr.Name, roots)
		if err != nil {
			return m, err
		}
		root := roots[prefix]
		mode := hdr.FileInfo().Mode()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if name != "." {
				if err := root.MkdirAll(name, 0o700); err != nil {
					return m, err
				}
			}
			dirsLater = append(dirsLater, later{root, name, hdr})
			continue
		case tar.TypeReg:
			if err := parent(root, name); err != nil {
				return m, err
			}
			f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return m, err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return m, fmt.Errorf("this backup is damaged or cut short: %w", err)
			}
		case tar.TypeSymlink:
			if err := parent(root, name); err != nil {
				return m, err
			}
			// The link is made as it was. Root keeps later entries from
			// being written through it to somewhere outside.
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return m, err
			}
		case tar.TypeLink:
			lp, target, err := split(hdr.Linkname, roots)
			if err != nil || lp != prefix {
				return m, fmt.Errorf("this backup has a bad link at %s", hdr.Name)
			}
			if err := parent(root, name); err != nil {
				return m, err
			}
			if err := root.Link(target, name); err != nil {
				return m, err
			}
			continue
		default:
			continue
		}
		if err := root.Lchown(name, hdr.Uid, hdr.Gid); err != nil {
			return m, err
		}
		if hdr.Typeflag != tar.TypeSymlink {
			if err := root.Chmod(name, mode.Perm()|mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)); err != nil {
				return m, err
			}
			_ = root.Chtimes(name, hdr.ModTime, hdr.ModTime)
		}
	}
	// Folders last, deepest first, so their own times and permissions
	// aren't changed by what went in them.
	for i := len(dirsLater) - 1; i >= 0; i-- {
		d := dirsLater[i]
		mode := d.hdr.FileInfo().Mode()
		if err := d.root.Lchown(d.name, d.hdr.Uid, d.hdr.Gid); err != nil {
			return m, err
		}
		if err := d.root.Chmod(d.name, mode.Perm()|mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)); err != nil {
			return m, err
		}
		_ = d.root.Chtimes(d.name, d.hdr.ModTime, d.hdr.ModTime)
	}
	return m, nil
}

// split finds which tree an archive name belongs to, and the path in it.
func split(name string, roots map[string]*os.Root) (prefix, rel string, err error) {
	name = strings.TrimSuffix(name, "/")
	for p := range roots {
		if name == p {
			return p, ".", nil
		}
		if rest, ok := strings.CutPrefix(name, p+"/"); ok {
			rest = path.Clean(rest)
			if !filepath.IsLocal(rest) {
				break
			}
			return p, rest, nil
		}
	}
	return "", "", fmt.Errorf("this backup has %q, which isn't part of the app", name)
}

func parent(root *os.Root, name string) error {
	if dir := path.Dir(name); dir != "." {
		return root.MkdirAll(dir, 0o700)
	}
	return nil
}

// Contents is what reading a whole backup found.
type Contents struct {
	Files int    `json:"files"`
	Bytes uint64 `json:"bytes"`
}

// Verify reads a whole archive without writing anything, which proves it
// can be restored: the key opens it, and every byte decrypts and
// decompresses as it was written.
func Verify(r io.Reader, identity string) (Manifest, Contents, error) {
	var m Manifest
	var c Contents
	in, err := decrypt(r, identity)
	if err != nil {
		return m, c, err
	}
	gz, err := gzip.NewReader(in)
	if err != nil {
		return m, c, fmt.Errorf("this isn't an opsie backup, or it's damaged: %w", err)
	}
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil || hdr.Name != manifestName {
		return m, c, errors.New("this isn't an opsie backup: it has no description of what's in it")
	}
	if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m); err != nil {
		return m, c, fmt.Errorf("this backup's description is damaged: %w", err)
	}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, c, fmt.Errorf("this backup is damaged or cut short: %w", err)
		}
		n, err := io.Copy(io.Discard, tr)
		if err != nil {
			return m, c, fmt.Errorf("this backup is damaged or cut short at %s: %w", hdr.Name, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			c.Files++
			c.Bytes += uint64(n)
		}
	}
	// gzip checks its own checksum at the very end.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return m, c, fmt.Errorf("this backup is damaged: %w", err)
	}
	return m, c, nil
}

// ReadManifest reads only the description at the start of an archive.
func ReadManifest(r io.Reader, identity string) (Manifest, error) {
	var m Manifest
	_, err := Extract(r, identity, func(got Manifest) (map[string]string, error) {
		m = got
		return nil, errStop
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return m, err
}

var errStop = errors.New("stop")

func decrypt(r io.Reader, identity string) (io.Reader, error) {
	// age files start with this line; anything else is a plain archive.
	head := make([]byte, len(ageMagic))
	n, _ := io.ReadFull(r, head)
	r = io.MultiReader(strings.NewReader(string(head[:n])), r)
	if string(head[:n]) != ageMagic {
		return r, nil
	}
	if strings.TrimSpace(identity) == "" {
		return nil, ErrNeedKey
	}
	ids, err := ParseIdentities(identity)
	if err != nil {
		return nil, err
	}
	out, err := age.Decrypt(r, ids...)
	var none *age.NoIdentityMatchError
	if errors.As(err, &none) {
		return nil, ErrWrongKey
	}
	return out, err
}

const ageMagic = "age-encryption.org/v1"

// NewKey makes a key pair: the secret key that opens backups, and the
// public key they're encrypted to.
func NewKey() (secret, public string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return id.String(), id.Recipient().String(), nil
}

// ParseRecipient reads a public key, like age1….
func ParseRecipient(s string) (age.Recipient, error) {
	rs, err := age.ParseRecipients(strings.NewReader(strings.TrimSpace(s)))
	if err != nil || len(rs) != 1 {
		return nil, errors.New("that isn't an age public key; it's one word starting with age1")
	}
	return rs[0], nil
}

// ParseIdentities reads secret keys: one, or several from a key file
// where each new key was added. Spaces, line breaks and comment lines are
// ignored, so a key copied off a screen where it wrapped still works, and
// so is text around it.
func ParseIdentities(s string) ([]age.Identity, error) {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			b.WriteString(strings.Join(strings.Fields(line), ""))
		}
	}
	var ids []age.Identity
	for _, k := range secretRe.FindAllString(strings.ToUpper(b.String()), -1) {
		id, err := age.ParseX25519Identity(k)
		if err != nil {
			return nil, errors.New("that secret key is damaged; check it was copied whole")
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("that isn't an age secret key; it starts with " + secretPrefix)
	}
	return ids, nil
}

// secretRe is an X25519 secret key: the prefix and 58 bech32 characters.
var secretRe = regexp.MustCompile(secretPrefix + `[QPZRY9X8GF2TVDW0S3JN54KHCE6MUA7L]{58}`)

const secretPrefix = "AGE-SECRET-KEY-1"

// PublicKeys are the public keys of the secret keys in s.
func PublicKeys(s string) []string {
	ids, err := ParseIdentities(s)
	if err != nil {
		return nil
	}
	var out []string
	for _, id := range ids {
		if x, ok := id.(*age.X25519Identity); ok {
			out = append(out, x.Recipient().String())
		}
	}
	return out
}
