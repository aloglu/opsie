package cron

import (
	"testing"
	"time"
)

func TestParseFile(t *testing.T) {
	text := `# comment
SHELL=/bin/sh
PATH = /usr/bin
30 3 * * 0 root test -e /run/systemd/system || SERVICE_MODE=1 /usr/libexec/e2scrub_all_cron
@reboot  root  /usr/local/bin/hello   --now
5 *
`
	jobs := ParseFile(text, "/etc/cron.d/x", "", true)
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs: %+v", len(jobs), jobs)
	}
	if j := jobs[0]; j.Schedule != "30 3 * * 0" || j.User != "root" || j.Line != 4 || j.Command[:7] != "test -e" {
		t.Errorf("first: %+v", j)
	}
	if j := jobs[1]; j.Schedule != "@reboot" || j.Command != "/usr/local/bin/hello --now" {
		t.Errorf("second: %+v", j)
	}
	user := ParseFile("*/10 * * * * backup.sh\n", "crontabs/debian", "debian", false)
	if len(user) != 1 || user[0].User != "debian" || user[0].Command != "backup.sh" {
		t.Errorf("user crontab: %+v", user)
	}
}

func TestNext(t *testing.T) {
	from := time.Date(2026, 11, 10, 14, 7, 30, 0, time.UTC) // a Tuesday
	for _, c := range []struct{ sched, want string }{
		{"* * * * *", "2026-11-10 14:08"},
		{"*/10 * * * *", "2026-11-10 14:10"},
		{"0 * * * *", "2026-11-10 15:00"},
		{"30 3 * * 0", "2026-11-15 03:30"},
		{"30 3 * * 7", "2026-11-15 03:30"},
		{"0 0 1 * *", "2026-12-01 00:00"},
		{"@yearly", "2027-01-01 00:00"},
		{"0 9 * * mon-fri", "2026-11-11 09:00"},
		{"0 0 29 2 *", "2028-02-29 00:00"},
		{"0 12 13 * 5", "2026-11-13 12:00"}, // the 13th or a Friday
		{"15 14 * * *", "2026-11-10 14:15"}, {"5 14 * * *", "2026-11-11 14:05"},
	} {
		got := Next(c.sched, from)
		if got.Format("2006-01-02 15:04") != c.want {
			t.Errorf("%s: got %v, want %s", c.sched, got, c.want)
		}
	}
	for _, bad := range []string{"@reboot", "61 * * * *", "* *", "0 0 31 2 *"} {
		if got := Next(bad, from); !got.IsZero() {
			t.Errorf("%s: got %v, want never", bad, got)
		}
	}
}

func TestDescribe(t *testing.T) {
	for in, want := range map[string]string{
		"30 3 * * 0":  "every Sunday at 03:30",
		"*/5 * * * *": "every 5 minutes",
		"7 * * * *":   "every hour at :07",
		"0 4 * * *":   "every day at 04:00",
		"0 4 2 * *":   "on day 2 of every month at 04:00",
		"@reboot":     "when the server starts",
		"1-5 4 * * *": "1-5 4 * * *",
	} {
		if got := Describe(in); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}
