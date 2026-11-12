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
	"time"
)

// The "GET Object" example from AWS's Signature Version 4 documentation.
func TestSignMatchesAWSExample(t *testing.T) {
	s := S3{Region: "us-east-1", KeyID: "AKIAIOSFODNN7EXAMPLE", Secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	s.sign(req, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestObjectURL(t *testing.T) {
	aws := S3{Endpoint: "https://s3.eu-west-1.amazonaws.com", Bucket: "mine", Prefix: "harbor/"}
	if u, _ := aws.objectURL("a.tar.gz"); u.String() != "https://mine.s3.eu-west-1.amazonaws.com/harbor/a.tar.gz" {
		t.Errorf("aws %s", u)
	}
	b2 := S3{Endpoint: "https://s3.eu-central-003.backblazeb2.com/", Bucket: "mine"}
	if u, _ := b2.objectURL("a.tar.gz"); u.String() != "https://s3.eu-central-003.backblazeb2.com/mine/a.tar.gz" {
		t.Errorf("b2 %s", u)
	}
	if _, err := (S3{Endpoint: "http://insecure.example"}).objectURL("x"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Error("plain http allowed")
	}
}

func TestPut(t *testing.T) {
	var got struct {
		path, auth string
		body       []byte
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth = r.URL.Path, r.Header.Get("Authorization")
		got.body, _ = io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "refuse") {
			w.WriteHeader(403)
			io.WriteString(w, "<Error><Code>AccessDenied</Code><Message>Key is not allowed to write here</Message></Error>")
		}
	}))
	defer srv.Close()
	client = srv.Client()
	file := filepath.Join(t.TempDir(), "app-20261110-030000.tar.gz.age")
	os.WriteFile(file, []byte("secret bytes"), 0o600)
	s := S3{Endpoint: srv.URL, Bucket: "b", Prefix: "harbor", KeyID: "id", Secret: "s"}
	if err := s.Put(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if got.path != "/b/harbor/app-20261110-030000.tar.gz.age" || string(got.body) != "secret bytes" || !strings.HasPrefix(got.auth, "AWS4-HMAC-SHA256 Credential=id/") {
		t.Errorf("got %+v", got)
	}
	s.Prefix = "refuse"
	if err := s.Put(context.Background(), file); err == nil || !strings.Contains(err.Error(), "isn't allowed to write there") {
		t.Errorf("refusal: %v", err)
	}
}

func TestValidSSH(t *testing.T) {
	for s, want := range map[string]bool{
		"backup@nas.example:/srv/backups/harbor": true, "me@10.0.0.2:backups": true,
		"-oProxyCommand=x@h:/p": false, "me@host:/srv/a;rm -rf /": false, "host:/srv": false, "me@host:/a b": false,
	} {
		if ValidSSH(s) != want {
			t.Errorf("%q: want %v", s, want)
		}
	}
}

func TestS3Error(t *testing.T) {
	for body, want := range map[string]string{
		"<Error><Code>SignatureDoesNotMatch</Code><Message>The request signature...</Message></Error>": "the key ID or secret is wrong",
		"<Error><Code>NoSuchBucket</Code><Message>x</Message></Error>":                                 "there's no bucket by that name",
		"<Error><Code>SlowDown</Code><Message>Please slow down</Message></Error>":                      "Please slow down",
		"": "no reason given",
	} {
		if got := s3Error(body); got != want {
			t.Errorf("s3Error(%q) = %q, want %q", body, got, want)
		}
	}
}
