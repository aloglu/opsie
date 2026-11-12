package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/aloglu/opsie/internal/priv"
)

// Every action the dashboard, the checks or the background service can
// ask for has to exist in the helper, or it fails only when someone taps it.
func TestEveryOpExists(t *testing.T) {
	opRe := regexp.MustCompile(`(?:Op:\s*|Call\(ctx, |Call\(context\.Background\(\), |n\.call\(ctx, |\bcall\(|cachedCall\(ctx, admin, )"([a-z0-9]+\.[a-z0-9.-]+)"`)
	var files []string
	for _, dir := range []string{".", "../health", "../daemon", "../facts"} {
		m, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		files = append(files, m...)
	}
	seen := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range opRe.FindAllStringSubmatch(string(data), -1) {
			seen++
			if !priv.Known(m[1]) {
				t.Errorf("%s asks for %q, which the helper doesn't have", filepath.Base(f), m[1])
			}
		}
	}
	t.Logf("checked %d", seen)
	if seen < 40 {
		t.Errorf("only found %d operations; the pattern is probably out of date", seen)
	}
}
