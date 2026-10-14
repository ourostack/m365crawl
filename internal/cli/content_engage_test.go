package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/engagecontent"
)

func TestEngageClockOverflowCLIAndSQL(t *testing.T) {
	e := cloudCLIEnv(t)
	engageControlled(t, &engageFixturePage{})
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	for _, raw := range []string{"9999-12-31T23:30:00-01:00", "9000-01-01T00:00:00Z", "0000-01-01T00:30:00+01:00", "0001-01-01T00:00:00Z"} {
		observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
			r := engageCLIResult("A")
			at, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				t.Fatal(err)
			}
			r.Threads[0].StarterUpdatedRaw = &raw
			r.Threads[0].StarterUpdatedAt = &at
			r.Losses[0].Count = 3
			return r, nil
		}
		if code, _, stderr := e.run("content", "fetch", "engage-home"); code != 0 {
			t.Fatal(stderr)
		}
	}
	for _, args := range [][]string{
		{"content", "list"}, {"search", "syntheticword", "--source", "content"},
		{"content", "list", "--modified-since", "9000-01-01T00:00:00Z"},
		{"search", "syntheticword", "--source", "content", "--since", "9000-01-01T00:00:00Z"},
	} {
		code, out, stderr := e.run(args...)
		if code != 0 {
			t.Fatal(stderr)
		}
		items := decode(t, out)["items"].([]any)
		if len(items) < 2 || items[0].(map[string]any)["starter_updated_raw"] != "9999-12-31T23:30:00-01:00" || items[1].(map[string]any)["starter_updated_raw"] != "9000-01-01T00:00:00Z" {
			t.Fatal("UTC-spill lexical/zero clock ordering:", out)
		}
	}
	for _, id := range []string{"engage-observation:1", "engage-observation:3", "engage-observation:4"} {
		code, out, stderr := e.run("--fields", "id,starter_updated_at,starter_updated_raw,full_text_sql", "content", "show", id)
		if code != 0 {
			t.Fatal(stderr)
		}
		item := decode(t, out)["items"].([]any)[0].(map[string]any)
		if item["starter_updated_at"] == nil {
			t.Fatal("known clock collapsed to null")
		}
		if code, full, stderr := e.run("--max-age", "0", "sql", item["full_text_sql"].(string)); code != 0 || !strings.Contains(full, item["starter_updated_raw"].(string)) {
			t.Fatalf("%s %s", full, stderr)
		}
	}
}

func TestEngageCLIReadCorruptionCannotHide(t *testing.T) {
	for _, statement := range []string{
		`delete from engage_fts where rowid=1`,
		`update engage_occurrences set starter_modified_at='corrupt'`,
		`delete from engage_capture_members where capture_id=2`,
		`update knowledge_attempts set losses_json='{}' where id=2`,
	} {
		t.Run(statement, func(t *testing.T) {
			e := cloudCLIEnv(t)
			engageControlled(t, &engageFixturePage{})
			saved := observeEngageHome
			t.Cleanup(func() { observeEngageHome = saved })
			observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
				return engageCLIResult("A"), nil
			}
			for i := 0; i < 3; i++ {
				if code, _, stderr := e.run("content", "fetch", "engage-home"); code != 0 {
					t.Fatal(stderr)
				}
			}
			db, err := sql.Open("sqlite", e.db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			for _, args := range [][]string{
				{"content", "search", "absent"}, {"content", "list", "--source-id", "999", "--limit", "1"},
				{"content", "sources"}, {"content", "show", "engage-observation:999"}, {"search", "absent", "--source", "content", "--limit", "1"},
			} {
				if code, _, _ := e.run(args...); code == 0 {
					t.Fatalf("corruption hidden by %v", args)
				}
			}
		})
	}
}

func TestEngageMixedFamilyTieOrdering(t *testing.T) {
	zero := time.Time{}
	future := zero.Add(time.Second)
	hits := []searchHit{
		{at: zero, engage: true, engageKnown: true, source: searchContent, cloudLabel: "x", cloudSourceID: 1, engageSessionID: 2, engageOrdinal: 0, cloudObservationID: 4, item: "engage-session2"},
		{at: zero, engage: true, engageKnown: true, source: searchContent, cloudLabel: "x", cloudSourceID: 1, engageSessionID: 1, engageOrdinal: 1, cloudObservationID: 3, item: "engage-ordinal1"},
		{at: zero, engage: true, engageKnown: true, source: searchContent, cloudLabel: "x", cloudSourceID: 1, engageSessionID: 1, engageOrdinal: 0, cloudObservationID: 2, item: "engage-ordinal0"},
		{at: future, source: searchChats, item: "legacy-chat"},
		{at: future, source: searchContent, item: "legacy-cloud"},
		{at: future, source: searchContent, localText: true, item: "legacy-local"},
		{at: future, engage: true, engageKnown: true, source: searchContent, item: "engage-known"},
		{source: searchContent, engage: true, item: "engage-unknown"},
	}
	sortSearchHits(hits)
	got := []any{}
	for _, h := range hits {
		got = append(got, h.item)
	}
	b, _ := json.Marshal(got)
	if string(b) != `["legacy-chat","legacy-cloud","legacy-local","engage-known","engage-ordinal0","engage-ordinal1","engage-session2","engage-unknown"]` {
		t.Fatal(string(b))
	}
}

func TestEngageFirstFailureAndAbsentCoverage(t *testing.T) {
	e := cloudCLIEnv(t)
	if code, _, _ := e.run("content", "show", "engage-observation:1"); code == 0 {
		t.Fatal("missing archive detail success")
	}
	for _, args := range [][]string{{"content", "sources"}, {"content", "list"}, {"content", "search", "absent"}, {"search", "absent", "--source", "content"}} {
		if code, out, stderr := e.run(args...); code != 0 || !strings.Contains(out, "engage_content_coverage") {
			t.Fatalf("%v %s %s", args, out, stderr)
		}
	}
	engageControlled(t, &engageFixturePage{})
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
		return engagecontent.Result{}, &engagecontent.ReadError{Code: "identity_missing"}
	}
	if code, _, _ := e.run("content", "fetch", "engage-home"); code == 0 {
		t.Fatal("unverified success")
	}
	code, out, stderr := e.run("content", "search", "absent")
	if code != 0 || !strings.Contains(out, `"instances":[]`) || !strings.Contains(out, `"account_state":"unknown"`) {
		t.Fatalf("%s %s", out, stderr)
	}
}
