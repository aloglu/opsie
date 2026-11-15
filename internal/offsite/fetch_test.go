package offsite

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloglu/opsie/internal/config"
)

func TestNewest(t *testing.T) {
	var all []Remote
	for _, n := range []string{
		"wiki-20261101-033000.tar.gz.age", "wiki-20261101-033000.json",
		"wiki-20261114-033000.tar.gz.age", "wiki-20261114-033000.json",
		"my-app-20261110-120000.tar.gz",
		"opsie-settings-20261101-033000.toml.age", "opsie-settings-20261114-090000.toml.age",
	} {
		all = append(all, Remote{Source: "s3", Name: n})
	}
	var got []string
	for _, r := range Newest(all) {
		got = append(got, r.Name)
	}
	want := "my-app-20261110-120000.tar.gz opsie-settings-20261114-090000.toml.age wiki-20261114-033000.json wiki-20261114-033000.tar.gz.age"
	if strings.Join(got, " ") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, " "), want)
	}
}

func TestS3ListAndGet(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=id/") {
			w.WriteHeader(403)
			return
		}
		switch {
		case r.URL.Path == "/b" && r.URL.Query().Get("list-type") == "2" && r.URL.Query().Get("prefix") == "harbor/":
			if r.URL.Query().Get("continuation-token") == "" {
				io.WriteString(w, `<ListBucketResult><Contents><Key>harbor/wiki-20261114-033000.tar.gz.age</Key><Size>12</Size></Contents><Contents><Key>harbor/../../etc/passwd</Key><Size>1</Size></Contents><IsTruncated>true</IsTruncated><NextContinuationToken>more</NextContinuationToken></ListBucketResult>`)
				return
			}
			io.WriteString(w, `<ListBucketResult><Contents><Key>harbor/wiki-20261114-033000.json</Key><Size>3</Size></Contents><Contents><Key>harbor/notes.txt</Key><Size>3</Size></Contents></ListBucketResult>`)
		case r.URL.Path == "/b/harbor/wiki-20261114-033000.tar.gz.age":
			io.WriteString(w, "secret bytes")
		default:
			w.WriteHeader(404)
			io.WriteString(w, "<Error><Code>NoSuchKey</Code><Message>gone</Message></Error>")
		}
	}))
	defer srv.Close()
	client = srv.Client()
	b := config.Backup{S3: config.S3{Endpoint: srv.URL, Bucket: "b", Prefix: "harbor", KeyID: "id", Secret: "s"}}
	list, err := List(context.Background(), b, "s3", "")
	if err != nil {
		t.Fatal(err)
	}
	// Only what opsie puts there is listed, whatever else the bucket says it has.
	if len(list) != 2 || list[0].Name != "wiki-20261114-033000.json" || list[1].Size != 12 {
		t.Fatalf("list %+v", list)
	}
	dir := t.TempDir()
	if err := Fetch(context.Background(), b, list[1], dir, ""); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, list[1].Name)); string(data) != "secret bytes" {
		t.Errorf("fetched %q", data)
	}
	if err := Fetch(context.Background(), b, Remote{Source: "s3", Name: "gone-20261114-033000.tar.gz"}, dir, ""); err == nil {
		t.Error("a missing file was fetched")
	}
	if err := Fetch(context.Background(), b, Remote{Source: "s3", Name: "../x"}, dir, ""); err == nil {
		t.Error("a name that isn't a backup's was fetched")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*partial")); len(left) != 0 {
		t.Errorf("left behind %v", left)
	}
}

func TestSSHListAndFetch(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho \"ssh $*\" >> "+log+"\nprintf 'wiki-20261114-033000.tar.gz\\nrm -rf x\\n'\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "scp"), []byte("#!/bin/sh\necho \"scp $*\" >> "+log+"\nfor last; do :; done\necho data > \"$last\"\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	b := config.Backup{SSH: "backup@nas.example:/srv/backups", SSHPort: 2222}
	list, err := List(context.Background(), b, "ssh", "")
	if err != nil || len(list) != 1 {
		t.Fatalf("list %+v %v", list, err)
	}
	to := t.TempDir()
	if err := Fetch(context.Background(), b, list[0], to, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(to, "wiki-20261114-033000.tar.gz")); err != nil {
		t.Error(err)
	}
	data, _ := os.ReadFile(log)
	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "Port=2222", "-- backup@nas.example ls -1 -- '/srv/backups'", "backup@nas.example:/srv/backups/wiki-20261114-033000.tar.gz"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("calls lack %q:\n%s", want, data)
		}
	}
}

func TestPrune(t *testing.T) {
	have := map[string]bool{}
	for _, n := range []string{
		"wiki-20261101-033000.tar.gz.age", "wiki-20261101-033000.json",
		"wiki-20261107-033000.tar.gz.age", "wiki-20261107-033000.json",
		"wiki-20261114-033000.tar.gz.age", "wiki-20261114-033000.json",
		"wiki-old-20261001-033000.tar.gz", // another app, whose name starts the same
		"notes-20261001-033000.tar.gz",
		"opsie-settings-20261101-033000.toml.age", "opsie-settings-20261114-090000.toml.age",
		"holiday-photos.zip", // not opsie's
	} {
		have[n] = true
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/b":
			io.WriteString(w, "<ListBucketResult>")
			for n := range have {
				io.WriteString(w, "<Contents><Key>box/"+n+"</Key><Size>1</Size></Contents>")
			}
			io.WriteString(w, "</ListBucketResult>")
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/b/box/"):
			delete(have, strings.TrimPrefix(r.URL.Path, "/b/box/"))
			w.WriteHeader(204)
		default:
			w.WriteHeader(400)
		}
	}))
	defer srv.Close()
	client = srv.Client()
	b := config.Backup{S3: config.S3{Endpoint: srv.URL, Bucket: "b", Prefix: "box", KeyID: "id", Secret: "s"}}
	ctx := context.Background()
	before := len(have)
	// No limit, the description of a backup, and a name that isn't opsie's
	// delete nothing.
	for _, c := range []struct {
		name string
		keep int
	}{{"wiki-20261114-033000.tar.gz.age", 0}, {"wiki-20261114-033000.json", 1}, {"holiday-photos.zip", 1}, {"wiki-20261114-033000.tar.gz.age", 3}} {
		if err := Prune(ctx, b, c.name, c.keep, ""); err != nil || len(have) != before {
			t.Fatalf("%+v deleted something: %v, %d left", c, err, len(have))
		}
	}
	if err := Prune(ctx, b, "/home/me/opsie-backups/wiki-20261114-033000.tar.gz.age", 2, ""); err != nil {
		t.Fatal(err)
	}
	if have["wiki-20261101-033000.tar.gz.age"] || have["wiki-20261101-033000.json"] || len(have) != before-2 {
		t.Errorf("keeping 2 of wiki left %v", have)
	}
	if err := Prune(ctx, b, "opsie-settings-20261114-090000.toml.age", 1, ""); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"wiki-20261107-033000.tar.gz.age", "wiki-20261114-033000.json", "wiki-old-20261001-033000.tar.gz", "notes-20261001-033000.tar.gz", "opsie-settings-20261114-090000.toml.age", "holiday-photos.zip"} {
		if !have[n] {
			t.Errorf("%s is gone", n)
		}
	}
	if have["opsie-settings-20261101-033000.toml.age"] {
		t.Error("the older settings copy is still there")
	}
}

func TestPruneSSH(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho \"ssh $*\" >> "+log+"\nprintf 'wiki-20261101-033000.tar.gz\\nwiki-20261101-033000.json\\nwiki-20261114-033000.tar.gz\\nnotes.txt\\n'\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	b := config.Backup{SSH: "backup@nas.example:/srv/backups"}
	if err := Prune(context.Background(), b, "wiki-20261114-033000.tar.gz", 1, ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(log)
	if !strings.Contains(string(data), "-- backup@nas.example rm -f -- '/srv/backups/wiki-20261101-033000.json' '/srv/backups/wiki-20261101-033000.tar.gz'\n") {
		t.Errorf("calls:\n%s", data)
	}
	if strings.Contains(string(data), "notes.txt") || strings.Contains(string(data), "20261114-033000.tar.gz'") {
		t.Errorf("deleted too much:\n%s", data)
	}
}
