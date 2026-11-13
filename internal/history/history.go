// Package history keeps a minute-by-minute record of the server's load,
// memory, network and disks, written by the background checks and drawn
// by the dashboard.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/aloglu/opsie/internal/metrics"
)

const (
	file = "metrics.jsonl"
	// Every is how often a sample is kept.
	Every = time.Minute
	// Keep is how far back the record goes.
	Keep = 8 * 24 * time.Hour
	// trimAt is how many lines the file grows to before old ones go:
	// a day over Keep, so trimming happens about once a day.
	trimAt = int((Keep + 24*time.Hour) / Every)
)

// Sample is one minute. Percentages are 0–100; Load is per core; Rx, Tx
// and IO (read, then written) are bytes per second.
type Sample struct {
	At   time.Time `json:"t"`
	CPU  float64   `json:"cpu"`
	Mem  float64   `json:"mem"`
	Swap float64   `json:"swap,omitempty"`
	Load float64   `json:"load"`
	Rx   float64   `json:"rx"`
	Tx   float64   `json:"tx"`
	// IO is missing from samples older than the reading.
	IO    []float64          `json:"io,omitempty"`
	Disks map[string]float64 `json:"disks,omitempty"`
}

// FromSnapshot takes a sample's values from a reading.
func FromSnapshot(s metrics.Snapshot) Sample {
	out := Sample{At: s.At, CPU: s.CPU, Mem: s.Mem.Percent, Rx: s.Net.RxRate, Tx: s.Net.TxRate}
	if s.IO.Ready {
		out.IO = []float64{s.IO.ReadRate, s.IO.WriteRate}
	}
	if s.Swap.Total > 0 {
		out.Swap = s.Swap.Percent
	}
	if s.Cores > 0 {
		out.Load = s.Load[0] / float64(s.Cores)
	}
	if len(s.Disks) > 0 {
		out.Disks = map[string]float64{}
		for _, d := range s.Disks {
			out.Disks[d.Mount] = d.Percent
		}
	}
	return out
}

// Recorder appends samples, at most one per Every.
type Recorder struct {
	Dir   string
	last  time.Time
	lines int
}

// Add records s if a minute has passed since the last one.
func (r *Recorder) Add(s Sample) error {
	if !r.last.IsZero() && s.At.Sub(r.last) < Every-5*time.Second {
		return nil
	}
	path := filepath.Join(r.Dir, file)
	if r.lines == 0 {
		r.lines = countLines(path)
	}
	line, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	f.Close()
	if err != nil {
		return err
	}
	r.last = s.At
	r.lines++
	if r.lines > trimAt {
		return r.trim(path, s.At)
	}
	return nil
}

// trim drops samples older than Keep.
func (r *Recorder) trim(path string, now time.Time) error {
	samples, err := read(path, now.Add(-Keep))
	if err != nil {
		return err
	}
	var b bytes.Buffer
	for _, s := range samples {
		line, _ := json.Marshal(s)
		b.Write(append(line, '\n'))
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o644); err != nil {
		return err
	}
	r.lines = len(samples)
	return os.Rename(tmp, path)
}

func countLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return bytes.Count(data, []byte("\n"))
}

// Read returns the samples in dir from since on, oldest first.
func Read(dir string, since time.Time) ([]Sample, error) {
	return read(filepath.Join(dir, file), since)
}

func read(path string, since time.Time) ([]Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Sample
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var s Sample
		if json.Unmarshal(sc.Bytes(), &s) != nil || s.At.Before(since) {
			continue
		}
		out = append(out, s)
	}
	return out, sc.Err()
}

// Bucket is the samples in one stretch of time.
type Bucket struct {
	Avg, Max float64
	N        int
}

// Buckets splits [from, to) into n equal stretches and summarizes what
// pick returns for the samples in each; pick's false skips a sample.
func Buckets(samples []Sample, from, to time.Time, n int, pick func(Sample) (float64, bool)) []Bucket {
	out := make([]Bucket, n)
	if n <= 0 || !to.After(from) {
		return out
	}
	span := to.Sub(from)
	for _, s := range samples {
		if s.At.Before(from) || !s.At.Before(to) {
			continue
		}
		v, ok := pick(s)
		if !ok {
			continue
		}
		i := int(float64(s.At.Sub(from)) / float64(span) * float64(n))
		i = min(max(i, 0), n-1)
		b := &out[i]
		b.Avg = (b.Avg*float64(b.N) + v) / float64(b.N+1)
		b.Max = max(b.Max, v)
		b.N++
	}
	return out
}

// Stats are the lowest, average and highest of what pick returns.
func Stats(samples []Sample, since time.Time, pick func(Sample) (float64, bool)) (low, avg, high float64, n int) {
	for _, s := range samples {
		if s.At.Before(since) {
			continue
		}
		v, ok := pick(s)
		if !ok {
			continue
		}
		if n == 0 || v < low {
			low = v
		}
		high = max(high, v)
		avg = (avg*float64(n) + v) / float64(n+1)
		n++
	}
	return low, avg, high, n
}
