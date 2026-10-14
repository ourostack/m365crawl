package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

func TestEngageOfflineAndZeroHitCoverage(t *testing.T) {
	e := cloudCLIEnv(t)
	engageControlled(t, &engageFixturePage{})
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	for _, user := range []string{"A", "B"} {
		observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
			r := engageCLIResult(user)
			r.Threads[0].Blocks = make([]string, 101)
			r.Threads[0].Blocks[100] = "lastblockword"
			r.Threads[0].Blocks[0] = "syntheticword"
			return r, nil
		}
		if code, _, stderr := e.run("content", "fetch", "engage-home"); code != 0 {
			t.Fatal(stderr)
		}
	}
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
		return engagecontent.Result{}, &engagecontent.ReadError{Code: "unavailable"}
	}
	if code, _, _ := e.run("content", "fetch", "engage-home"); code == 0 {
		t.Fatal("failure hidden")
	}
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(e.db)
	if err != nil {
		t.Fatal(err)
	}
	openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		t.Fatal("ordinary read launched browser")
		return nil, nil, nil
	}
	for _, args := range [][]string{
		{"content", "list"}, {"content", "search", "absent"}, {"--fields", "id", "content", "search", "syntheticword"},
		{"content", "show", "engage-observation:1"}, {"content", "sources"}, {"search", "absent", "--source", "content"},
		{"--max-age", "1ns", "content", "list"}, {"--max-age", "1ns", "search", "syntheticword", "--source", "content"},
	} {
		code, out, stderr := e.run(args...)
		if code != 0 || !strings.Contains(out, "engage_content_coverage") {
			t.Fatalf("%v: %s %s", args, out, stderr)
		}
		doc := decode(t, out)
		coverage := doc["engage_content_coverage"].(map[string]any)
		wantInstances := 2
		if len(args) > 1 && args[1] == "show" {
			wantInstances = 1
		}
		if len(coverage["instances"].([]any)) != wantInstances || len(coverage["registrations"].([]any)) != 1 {
			t.Fatalf("coverage lost %v: %s", args, out)
		}
		reg := coverage["registrations"].([]any)[0].(map[string]any)
		if reg["account_state"] != "unknown" {
			t.Fatal(reg)
		}
		if strings.Contains(out, "lastblockword") {
			t.Fatal("coverage/preview leaked full Result")
		}
	}
	code, out, stderr := e.run("content", "show", "engage-observation:1")
	if code != 0 {
		t.Fatal(stderr)
	}
	item := decode(t, out)["items"].([]any)[0].(map[string]any)
	if code, full, stderr := e.run("--max-age", "0", "sql", item["full_text_sql"].(string)); code != 0 || !strings.Contains(full, "lastblockword") {
		t.Fatalf("full SQL %s %s", full, stderr)
	}
	after, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	end, err := os.Stat(e.db)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !stat.ModTime().Equal(end.ModTime()) {
		t.Fatal("offline read mutated archive")
	}
}

// Known Go-zero is a present bound; unknown native clocks must not match it.
func TestEngageCLIExplicitZeroDateBound(t *testing.T) {
	e := cloudCLIEnv(t)
	engageControlled(t, &engageFixturePage{})
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
		return engageCLIResult("A"), nil
	}
	if code, _, stderr := e.run("content", "fetch", "engage-home"); code != 0 {
		t.Fatal(stderr)
	}
	for _, args := range [][]string{{"content", "list", "--modified-since", "0001-01-01T00:00:00Z"}, {"search", "syntheticword", "--source", "content", "--since", "0001-01-01T00:00:00Z"}} {
		code, out, stderr := e.run(args...)
		if code != 0 || decode(t, out)["count"] != float64(0) {
			t.Fatalf("%v %s %s", args, out, stderr)
		}
	}
	for _, args := range [][]string{{"content", "list"}, {"search", "syntheticword", "--source", "content"}} {
		code, out, stderr := e.run(args...)
		if code != 0 || decode(t, out)["count"] != float64(1) || !strings.Contains(out, "engage_content_coverage") {
			t.Fatalf("%v %s %s", args, out, stderr)
		}
	}
	_ = time.Second
}
