// Package offsite copies backups off the server: to another machine over
// SSH, or to S3-compatible storage like Backblaze B2 or Cloudflare R2.
package offsite

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// S3 is a bucket, and the key to write to it.
type S3 struct {
	Endpoint string // like https://s3.eu-central-003.backblazeb2.com
	Region   string // "auto" for R2
	Bucket   string
	Prefix   string // folder in the bucket
	KeyID    string
	Secret   string
}

// maxPut is the most S3 takes in one upload.
const maxPut = 5 << 30

var client = &http.Client{Timeout: 6 * time.Hour}

// objectURL is where a key goes: virtual-hosted on AWS, by path
// elsewhere, which every S3-compatible service accepts.
func (s S3) objectURL(key string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(s.Endpoint, "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("the S3 endpoint %q should be an https:// address", s.Endpoint)
	}
	obj := strings.Trim(path.Join(s.Prefix, key), "/")
	if strings.HasSuffix(u.Host, ".amazonaws.com") && !strings.Contains(s.Bucket, ".") {
		u.Host = s.Bucket + "." + u.Host
		u.Path = "/" + obj
	} else {
		u.Path = "/" + s.Bucket + "/" + obj
	}
	return u, nil
}

// Put uploads a file.
func (s S3) Put(ctx context.Context, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxPut {
		return fmt.Errorf("%s is over 5 GB, more than S3 takes in one go", path.Base(file))
	}
	u, err := s.objectURL(path.Base(file))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), f)
	if err != nil {
		return err
	}
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	s.sign(req, "UNSIGNED-PAYLOAD", time.Now().UTC())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("the storage refused the upload (%s): %s", resp.Status, s3Error(string(body)))
	}
	return nil
}

// s3Error picks the message out of S3's XML error.
func s3Error(body string) string {
	if _, rest, ok := strings.Cut(body, "<Code>"); ok {
		code, _, _ := strings.Cut(rest, "</Code>")
		switch code {
		case "SignatureDoesNotMatch", "InvalidAccessKeyId":
			return "the key ID or secret is wrong"
		case "NoSuchBucket":
			return "there's no bucket by that name"
		case "AccessDenied":
			return "this key isn't allowed to write there"
		}
	}
	if _, rest, ok := strings.Cut(body, "<Message>"); ok {
		if msg, _, ok := strings.Cut(rest, "</Message>"); ok {
			return msg
		}
	}
	if body == "" {
		return "no reason given"
	}
	return strings.TrimSpace(body)
}

// sign adds AWS Signature Version 4 to req.
func (s S3) sign(req *http.Request, payloadHash string, now time.Time) {
	region := s.Region
	if region == "" {
		region = "us-east-1"
	}
	stamp := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("x-amz-date", stamp)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	host := req.URL.Host
	// Every x-amz header, plus host, range and content type, are signed.
	headers := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") || lk == "range" || lk == "content-type" {
			headers[lk] = strings.TrimSpace(strings.Join(v, ","))
		}
	}
	var names []string
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{req.Method, escapePath(req.URL.Path), canonicalQuery(req.URL.Query()),
		canonHeaders.String(), signed, payloadHash}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hexSum(canonical)
	key := hmacSum([]byte("AWS4"+s.Secret), day)
	key = hmacSum(key, region)
	key = hmacSum(key, "s3")
	key = hmacSum(key, "aws4_request")
	sig := hex.EncodeToString(hmacSum(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.KeyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func hexSum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func hmacSum(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}

// escapePath encodes each part of a path the way S3 expects.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = escape(s)
	}
	return strings.Join(parts, "/")
}

func escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func canonicalQuery(q url.Values) string {
	var parts []string
	for k, vs := range q {
		for _, v := range vs {
			parts = append(parts, escape(k)+"="+escape(v))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

// ErrNoDestination is returned when copies aren't set up.
var ErrNoDestination = errors.New("no place to copy backups to is set")
