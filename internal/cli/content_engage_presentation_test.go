package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestEngageTextRoutesAndProjectedIntegers(t *testing.T) {
	e := cloudCLIEnv(t)
	engageControlled(t, &engageFixturePage{})
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
		r := engageCLIResult("A")
		title := "Synthetic native label"
		r.Threads[0].Title = &title
		return r, nil
	}
	if code, _, stderr := e.run("content", "fetch", "engage-home"); code != 0 {
		t.Fatal(stderr)
	}
	for _, args := range [][]string{{"content", "list"}, {"content", "list", "--source-id", "1"}, {"content", "show", "engage-observation:1"}, {"content", "sources"}, {"content", "sources", "--source-id", "1"}, {"search", "syntheticword", "--source", "content"}} {
		code, out, stderr := e.run(append([]string{"--format", "text"}, args...)...)
		if code != 0 || !strings.Contains(out, "Engage") {
			t.Fatalf("%v: %s %s", args, out, stderr)
		}
		if len(args) > 1 && args[1] == "show" && !strings.Contains(out, "original session") {
			t.Fatal("own loss label missing")
		}
	}
	row := store.EngageObservationRow{ID: 9007199254740993, SourceID: 9007199254740993, SessionID: 9007199254740993, Thread: engageCLIResult("A").Threads[0]}
	item := shapeEngage(row, 0)
	p, err := project(item, []string{"id", "source_id", "session_id", "native_thread_id"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"source_id":9007199254740993`) || !strings.Contains(string(b), `"session_id":9007199254740993`) {
		t.Fatal("projection rounded IDs:", string(b))
	}
	for _, value := range []string{"other:1", "engage-observation:0", "engage-observation:-1", "engage-observation:9223372036854775808", "engage-observation:"} {
		if _, err := engageObservationID(value); err == nil {
			t.Fatal("invalid positive ID accepted")
		}
		if code, _, _ := e.run("content", "show", "engage-observation:0"); code == 0 {
			t.Fatal("zero ID admitted")
		}
		if code, _, _ := e.run("content", "sources", "--source-id", "999"); code == 0 {
			t.Fatal("unknown source admitted")
		}
	}
}
