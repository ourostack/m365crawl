//go:build acceptance

package acceptance

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const costCaseEnv = "M365CRAWL_ACCEPTANCE_COST_CASE"

func outlookCostTestRoot(t *testing.T) string {
	t.Helper()
	root := calendarScratch(t)
	t.Setenv("TMPDIR", root)
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	return root
}

func TestOutlookCostPolicy(t *testing.T) {
	outlookCostTestRoot(t)
	t.Setenv(watchEnv, "1")
	for _, tc := range []struct {
		name    string
		fails   bool
		want    []string
		notWant []string
	}{
		{
			name: "large",
			want: []string{"160.0 MiB", "180.0 MiB", "9s"},
		},
		{
			name:  "forced-failure",
			fails: true,
			want:  []string{"forced Outlook-only re-read failed"},
		},
		{
			name:  "measurement-failure",
			fails: true,
			want:  []string{"cost could not be measured"},
		},
		{
			name:    "unavailable",
			want:    []string{"resource metrics unavailable", "peak RSS is not available"},
			notWant: []string{"peak RSS 0.0 MiB", "+0.0 MiB"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestOutlookCostSyntheticChild$", "-test.v", "-test.count=1") //nolint:gosec // this test binary
			cmd.Env = append(os.Environ(), costCaseEnv+"="+tc.name)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fails {
				t.Fatalf("failure=%v, want %v:\n%s", err, tc.fails, out)
			}
			for _, want := range tc.want {
				if !strings.Contains(string(out), want) {
					t.Errorf("missing %q:\n%s", want, out)
				}
			}
			for _, bad := range tc.notWant {
				if strings.Contains(string(out), bad) {
					t.Errorf("unavailable metrics reported as %q:\n%s", bad, out)
				}
			}
		})
	}
}

func TestOutlookCostSyntheticChild(t *testing.T) {
	mode := os.Getenv(costCaseEnv)
	if mode == "" {
		t.Skip("runs only in an isolated synthetic cost process")
	}
	t.Setenv("M365CRAWL_REAL_CACHE", "1")
	t.Setenv(watchEnv, "")
	t.Setenv(outlookRootEnv, t.TempDir())
	teamsOnce.Do(func() {})
	outlookOnce.Do(func() {})
	outlookRun = outlookRunData{own: ownCost{ok: true, first: 100 << 20, baseline: 20 << 20}}
	scanOnce.done = true
	if mode == "large" {
		scanOnce.s.info.Size = 160 << 20
		scanOnce.s.scanWall = 9 * time.Second
		outlookRun.own.first = 200 << 20
	}
	switch mode {
	case "forced-failure":
		outlookRun.forcedErr = "archive_invalid"
	case "measurement-failure":
		outlookRun.own.failure = "synthetic child failed"
	case "unavailable":
		outlookRun.own = ownCost{}
	}
	TestRealOutlookCost(t)
}

func TestOutlookCostChildRejectsUnexpectedSyncFailure(t *testing.T) {
	outlookCostTestRoot(t)
	for _, mode := range []string{"baseline", "outlook"} {
		t.Run(mode, func(t *testing.T) {
			_, _, ok, failure := childPeaks(t.TempDir(), t.TempDir(), mode)
			if failure == "" || ok {
				t.Fatalf("an archive-directory sync error was accepted: ok=%v failure=%q", ok, failure)
			}
		})
	}
}

func TestOutlookCostChildAcceptsExpectedBaselineAbsence(t *testing.T) {
	root := outlookCostTestRoot(t)
	_, _, _, failure := childPeaks(filepath.Join(root, "baseline.db"), t.TempDir(), "baseline")
	if failure != "" {
		t.Fatalf("expected no-source baseline failed: %s", failure)
	}
}

func TestOutlookCostChildProtocol(t *testing.T) {
	for _, tc := range []struct {
		name          string
		output        string
		runErr        error
		before, after int64
		available     bool
		fails         bool
	}{
		{name: "measured", output: "test framework output\nM365CRAWL-CHILD-RSS 123 456 none true\n", before: 123, after: 456, available: true},
		{name: "unavailable", output: "M365CRAWL-CHILD-RSS 0 0 none false\n"},
		{name: "partly unavailable", output: "M365CRAWL-CHILD-RSS 123 0 none false\n"},
		{name: "failed child", output: "M365CRAWL-CHILD-RSS 123 456 none true\n", runErr: errors.New("child exit 1"), fails: true},
		{name: "sync failed", output: "M365CRAWL-CHILD-RSS 123 456 archive_invalid true\n", fails: true},
		{name: "missing result", output: "PASS\n", fails: true},
		{name: "missing field", output: "M365CRAWL-CHILD-RSS 123 456 none\n", fails: true},
		{name: "extra field", output: "M365CRAWL-CHILD-RSS 123 456 none true extra\n", fails: true},
		{name: "bad before", output: "M365CRAWL-CHILD-RSS bad 456 none true\n", fails: true},
		{name: "bad after", output: "M365CRAWL-CHILD-RSS 123 bad none true\n", fails: true},
		{name: "overflow", output: "M365CRAWL-CHILD-RSS 123 9223372036854775808 none true\n", fails: true},
		{name: "negative", output: "M365CRAWL-CHILD-RSS -1 456 none true\n", fails: true},
		{name: "negative unavailable", output: "M365CRAWL-CHILD-RSS 0 -1 none false\n", fails: true},
		{name: "invalid availability", output: "M365CRAWL-CHILD-RSS 123 456 none unknown\n", fails: true},
		{name: "zero available", output: "M365CRAWL-CHILD-RSS 0 0 none true\n", fails: true},
		{name: "decreasing peak", output: "M365CRAWL-CHILD-RSS 456 123 none true\n", fails: true},
		{name: "duplicate result", output: "M365CRAWL-CHILD-RSS 123 456 none true\nM365CRAWL-CHILD-RSS 123 456 none true\n", fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after, available, failure := parseChildPeaks([]byte(tc.output), tc.runErr)
			if (failure != "") != tc.fails || available != tc.available {
				t.Fatalf("available=%v failure=%q, want available=%v failure=%v", available, failure, tc.available, tc.fails)
			}
			if !tc.fails && (before != tc.before || after != tc.after) {
				t.Fatalf("peaks %d/%d, want %d/%d", before, after, tc.before, tc.after)
			}
		})
	}
}

func TestOutlookCostTeamsMetrics(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after bool
		available     bool
	}{
		{name: "measured", before: true, after: true, available: true},
		{name: "before unavailable", after: true},
		{name: "after unavailable", before: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := teamsSyncMeasure(teamsRun{
				wall:   4 * time.Second,
				before: usage{cpu: 3 * time.Second, peakRSS: 4096, ok: tc.before},
				after:  usage{cpu: 5 * time.Second, peakRSS: 8192, ok: tc.after},
			})
			if got.ok != tc.available {
				t.Fatalf("availability=%v, want %v", got.ok, tc.available)
			}
			if got.ok && (got.cpu != 2*time.Second || got.wall != 4*time.Second || got.peakBefore != 4096 || got.peakAfter != 8192) {
				t.Fatalf("CPU=%v wall=%v peaks=%d/%d, want 2s/4s/4096/8192", got.cpu, got.wall, got.peakBefore, got.peakAfter)
			}
		})
	}
}
