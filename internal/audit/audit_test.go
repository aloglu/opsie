package audit

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for i, e := range []Entry{
		{Action: "unit.restart", Args: map[string]string{"unit": "a"}, Result: "requested"},
		{Action: "unit.restart", Args: map[string]string{"unit": "a"}, Result: "ok"},
		{Action: "docker.stop", Args: map[string]string{"container": "x"}, Result: "requested"},
		{Action: "apt.upgrade", Result: "requested"},
		{Action: "apt.upgrade", Result: "failed", Error: "locked"},
	} {
		e.Time = at.Add(time.Duration(i) * time.Minute)
		if err := Log(path, e); err != nil {
			t.Fatal(err)
		}
	}
	got := Read(path, 10)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Action != "apt.upgrade" || got[0].Result != "failed" || got[0].Error != "locked" {
		t.Errorf("newest %+v", got[0])
	}
	if got[1].Action != "docker.stop" || !got[1].Unfinished {
		t.Errorf("unfinished %+v", got[1])
	}
	if got[2].Result != "ok" {
		t.Errorf("oldest %+v", got[2])
	}
	if len(Read(path, 1)) != 1 {
		t.Error("limit ignored")
	}
}
