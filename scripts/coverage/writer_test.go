package main

import (
	"bytes"
	"errors"
	"testing"
)

type brokenGateWriter struct {
	at, calls int
}

func (w *brokenGateWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.at {
		return 0, errors.New("synthetic write failure")
	}
	return len(p), nil
}

func TestCoverageGateDoesNotReportSuccessAfterOutputFailure(t *testing.T) {
	root, profile := fixtureProfile(t, true, "github.com/ourostack/m365crawl")
	old := coverageScope
	t.Cleanup(func() { coverageScope = old })
	coverageScope = func(string) (map[string][]string, error) { return map[string][]string{"sample.go": nil}, nil }
	for _, at := range []int{1, 2} {
		var stderr bytes.Buffer
		if code := run([]string{"-root=" + root, "-profile=" + profile}, &brokenGateWriter{at: at}, &stderr); code != 1 || stderr.Len() == 0 {
			t.Fatalf("output failure %d became success: %d %s", at, code, stderr.String())
		}
	}
}
