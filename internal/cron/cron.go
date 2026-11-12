// Package cron reads crontab lines and works out when they run next.
package cron

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Job is one line of a crontab.
type Job struct {
	Schedule string `json:"schedule"` // the five fields, or @daily and the like
	User     string `json:"user"`
	Command  string `json:"command"`
	Source   string `json:"source"`
	Line     int    `json:"line"`
}

var shorthands = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var envRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*=`)

// ParseFile reads a crontab. System crontabs (/etc/crontab, /etc/cron.d)
// name the user on each line; a user's own crontab doesn't, so owner is
// used.
func ParseFile(text, source, owner string, system bool) []Job {
	var out []Job
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || envRe.MatchString(line) {
			continue
		}
		f := strings.Fields(line)
		n := 5
		if strings.HasPrefix(f[0], "@") {
			n = 1
		}
		j := Job{Schedule: strings.Join(f[:min(n, len(f))], " "), User: owner, Source: source, Line: i + 1}
		if system {
			if len(f) <= n+1 {
				continue
			}
			j.User, n = f[n], n+1
		}
		if len(f) <= n {
			continue
		}
		j.Command = strings.Join(f[n:], " ")
		out = append(out, j)
	}
	return out
}

// spec is a parsed schedule: which minutes, hours, days, months and
// weekdays match.
type spec struct {
	min, hour, dom, month, dow [64]bool
	domAny, dowAny             bool
}

var names = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

func field(s string, lo, hi int, set *[64]bool) error {
	for _, part := range strings.Split(s, ",") {
		step := 1
		if r, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n <= 0 {
				return fmt.Errorf("bad step %q", part)
			}
			part, step = r, n
		}
		from, to := lo, hi
		if part != "*" {
			a, b, isRange := strings.Cut(part, "-")
			var err error
			if from, err = num(a); err != nil {
				return err
			}
			to = from
			if isRange {
				if to, err = num(b); err != nil {
					return err
				}
			} else if step > 1 {
				to = hi
			}
		}
		if from < lo || to > hi || from > to {
			return fmt.Errorf("%q is out of range", part)
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	return nil
}

func num(s string) (int, error) {
	if n, ok := names[strings.ToLower(s)]; ok {
		return n, nil
	}
	return strconv.Atoi(s)
}

func parse(schedule string) (*spec, error) {
	if s, ok := shorthands[schedule]; ok {
		schedule = s
	}
	f := strings.Fields(schedule)
	if len(f) != 5 {
		return nil, fmt.Errorf("%q isn't a schedule", schedule)
	}
	var s spec
	for i, r := range []struct {
		lo, hi int
		set    *[64]bool
	}{{0, 59, &s.min}, {0, 23, &s.hour}, {1, 31, &s.dom}, {1, 12, &s.month}, {0, 7, &s.dow}} {
		if err := field(f[i], r.lo, r.hi, r.set); err != nil {
			return nil, err
		}
	}
	s.dow[0] = s.dow[0] || s.dow[7]
	s.domAny, s.dowAny = f[2] == "*", f[4] == "*"
	return &s, nil
}

func (s *spec) day(t time.Time) bool {
	dom, dow := s.dom[t.Day()], s.dow[int(t.Weekday())]
	switch {
	case s.domAny && s.dowAny:
		return true
	case s.domAny:
		return dow
	case s.dowAny:
		return dom
	}
	return dom || dow // cron runs when either matches
}

// Next is when a schedule runs next after from, or zero for @reboot and
// schedules that never match.
func Next(schedule string, from time.Time) time.Time {
	s, err := parse(schedule)
	if err != nil {
		return time.Time{}
	}
	t := from.Truncate(time.Minute).Add(time.Minute)
	limit := from.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case !s.month[int(t.Month())]:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
		case !s.day(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
		case !s.hour[t.Hour()]:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
		case !s.min[t.Minute()]:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// Describe says a schedule in words when it's a common one, and returns
// it as it is otherwise.
func Describe(schedule string) string {
	switch schedule {
	case "@reboot":
		return "when the server starts"
	case "@hourly":
		return "every hour"
	case "@daily", "@midnight":
		return "every day at 00:00"
	case "@weekly":
		return "every Sunday at 00:00"
	case "@monthly":
		return "on the 1st of every month"
	case "@yearly", "@annually":
		return "every 1 January"
	}
	f := strings.Fields(schedule)
	if len(f) != 5 {
		return schedule
	}
	m, h, dom, mon, dow := f[0], f[1], f[2], f[3], f[4]
	isNum := func(s string) bool { _, err := strconv.Atoi(s); return err == nil }
	clock := func() string {
		hh, _ := strconv.Atoi(h)
		mm, _ := strconv.Atoi(m)
		return fmt.Sprintf("%02d:%02d", hh, mm)
	}
	switch {
	case m == "*" && h == "*" && dom == "*" && mon == "*" && dow == "*":
		return "every minute"
	case strings.HasPrefix(m, "*/") && h == "*" && dom == "*" && mon == "*" && dow == "*":
		return "every " + strings.TrimPrefix(m, "*/") + " minutes"
	case isNum(m) && h == "*" && dom == "*" && mon == "*" && dow == "*":
		return fmt.Sprintf("every hour at :%02s", m)
	case isNum(m) && isNum(h) && dom == "*" && mon == "*" && dow == "*":
		return "every day at " + clock()
	case isNum(m) && isNum(h) && dom == "*" && mon == "*" && isNum(dow):
		n, _ := strconv.Atoi(dow)
		return "every " + time.Weekday(n%7).String() + " at " + clock()
	case isNum(m) && isNum(h) && isNum(dom) && mon == "*" && dow == "*":
		return "on day " + dom + " of every month at " + clock()
	}
	return schedule
}
