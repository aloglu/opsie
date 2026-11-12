package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aloglu/opsie/internal/metrics"
)

func TestRecordReadAndTrim(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{Dir: dir}
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	// Ten days, one sample a minute, offered every 30 seconds.
	for i := 0; i < 10*24*60*2; i++ {
		at := start.Add(time.Duration(i) * 30 * time.Second)
		if err := r.Add(Sample{At: at, CPU: float64(i % 100), Disks: map[string]float64{"/": 50}}); err != nil {
			t.Fatal(err)
		}
	}
	end := start.Add(10 * 24 * time.Hour)
	all, err := Read(dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if oldest := end.Sub(all[0].At); oldest > Keep+25*time.Hour || oldest < Keep {
		t.Errorf("oldest sample is %v old", oldest)
	}
	if gap := all[1].At.Sub(all[0].At); gap != time.Minute {
		t.Errorf("samples %v apart", gap)
	}
	if info, _ := os.Stat(filepath.Join(dir, file)); info.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	day, _ := Read(dir, end.Add(-24*time.Hour))
	if len(day) < 1430 || len(day) > 1441 {
		t.Errorf("%d samples in a day", len(day))
	}
}

func TestBucketsAndStats(t *testing.T) {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var s []Sample
	for i := 0; i < 60; i++ {
		s = append(s, Sample{At: from.Add(time.Duration(i) * time.Minute), CPU: float64(i)})
	}
	cpu := func(x Sample) (float64, bool) { return x.CPU, true }
	b := Buckets(s, from, from.Add(time.Hour), 6, cpu)
	if b[0].N != 10 || b[0].Avg != 4.5 || b[0].Max != 9 || b[5].Max != 59 {
		t.Errorf("%+v", b)
	}
	// A stretch with nothing in it stays empty.
	b = Buckets(s, from, from.Add(2*time.Hour), 4, cpu)
	if b[3].N != 0 {
		t.Errorf("%+v", b[3])
	}
	low, avg, high, n := Stats(s, from.Add(30*time.Minute), cpu)
	if low != 30 || high != 59 || avg != 44.5 || n != 30 {
		t.Error(low, avg, high, n)
	}
}

func TestFromSnapshot(t *testing.T) {
	s := FromSnapshot(metrics.Snapshot{CPU: 40, Cores: 2, Load: [3]float64{3, 0, 0}, Mem: metrics.Memory{Percent: 60},
		Disks: []metrics.Disk{{Mount: "/", Percent: 47}}})
	if s.Load != 1.5 || s.Disks["/"] != 47 || s.Swap != 0 {
		t.Errorf("%+v", s)
	}
}
