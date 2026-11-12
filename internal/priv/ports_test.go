package priv

import (
	"strings"
	"testing"
)

func TestBindLocal(t *testing.T) {
	text := `services:
  db:
    ports:
      - "5432:5432"   # postgres
      - 8080:80
      - '27015:27015/udp'
      - 0.0.0.0:9000:9000
      - "${PORT}:3000"
`
	for _, c := range []struct{ port, proto, want string }{
		{"5432", "tcp", `      - "127.0.0.1:5432:5432"   # postgres`},
		{"8080", "tcp", `      - 127.0.0.1:8080:80`},
		{"27015", "udp", `      - '127.0.0.1:27015:27015/udp'`},
		{"9000", "tcp", `      - 127.0.0.1:9000:9000`},
	} {
		out, n := BindLocal(text, c.port, c.proto)
		if n != 1 || !strings.Contains(out, c.want) {
			t.Errorf("%s/%s: n=%d\n%s", c.port, c.proto, n, out)
		}
	}
	if _, n := BindLocal(text, "27015", "tcp"); n != 0 {
		t.Error("matched the wrong protocol")
	}
	if _, n := BindLocal(text, "3000", "tcp"); n != 0 {
		t.Error("matched a variable")
	}
	if _, n := BindLocal(text+"      - 5432:5433\n", "5432", "tcp"); n != 2 {
		t.Error("didn't notice the port twice")
	}
}

func TestParseUnixStamp(t *testing.T) {
	if got, ok := parseUnixStamp("@1793792067\n"); !ok || got.Unix() != 1793792067 {
		t.Errorf("got %v %v", got, ok)
	}
	for _, bad := range []string{"", "n/a", "@0"} {
		if _, ok := parseUnixStamp(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}
