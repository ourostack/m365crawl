package cli

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/m365crawl/internal/syncer"
)

func assertMessageMedia(t *testing.T, doc map[string]any, count int) {
	t.Helper()
	want := map[string]any{"returned_messages": float64(count), "scope": "returned_teams_messages",
		"binary_media": "not_included", "reference_inspection": "not_requested"}
	if !reflect.DeepEqual(doc["media_coverage"], want) {
		t.Fatalf("returned Teams message media scope/absence lost: got %v, want %v", doc["media_coverage"], want)
	}
}

func TestMessageMediaCoverageSurvivesProjectionAndWatch(t *testing.T) {
	e := appEnv(t)
	routes := []struct {
		name  string
		args  []string
		count int
	}{
		{"messages", []string{"messages", "--conversation", appChat}, 2},
		{"unread", []string{"unread", "--conversation", appChat}, 2},
		{"thread", []string{"thread", appChat, "app-1"}, 1},
		{"search", []string{"search", "evidence", "--source", "chats"}, 2},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			for _, fields := range [][]string{nil, {"--fields", "text"}} {
				args := append(append([]string(nil), route.args...), fields...)
				doc := jsonRun(t, e, args...)
				assertMessageMedia(t, doc, route.count)
				if len(fields) > 0 {
					for _, item := range items(t, doc) {
						if item["id"] != nil || item["html"] != nil || item["media_coverage"] != nil {
							t.Fatalf("projection changed or coverage leaked into items: %v", item)
						}
					}
				}
				for _, format := range []string{"text", "log"} {
					code, out, stderr := e.run(append([]string{"--no-sync", "--format", format}, args...)...)
					if code != 0 || stderr != "" {
						t.Fatalf("%s failed: %d %s", format, code, stderr)
					}
					if format == "text" {
						if !strings.Contains(out, "media bytes not included") || !strings.Contains(out, "reference inspection not requested") {
							t.Fatalf("text silently implied complete media: %s", out)
						}
					} else if !strings.Contains(out, "media_coverage") || !strings.Contains(out, "not_included") ||
						!strings.Contains(out, "not_requested") {
						t.Fatalf("log omitted media scope/absence: %s", out)
					}
				}
			}
		})
	}
	activity := items(t, jsonRun(t, e, "activity", "--limit", "1"))[0]
	activityKey := activity["tenant_id"].(string) + "|" + activity["user_id"].(string) + "|" + activity["id"].(string)
	changes := []syncer.Change{
		{Kind: "message", Change: "new", Key: tenantA + "|" + userA + "|" + appChat + "|app-1"},
		{Kind: "activity", Change: "new", Key: activityKey},
	}
	for _, format := range []output.Format{output.JSON, output.Log, output.Text} {
		w, out, _ := jsonWatcher(t, e.root, e.db)
		w.rt.fields, w.rt.format = []string{"text"}, format
		if err := w.emit(syncer.Report{Status: "ok"}, changes); err != nil {
			t.Fatal(err)
		}
		if format == output.Text {
			if strings.Count(out.String(), "media bytes not included") != 1 {
				t.Fatalf("watch text covered non-message events or hid message media: %s", out.String())
			}
			continue
		}
		lines := out.lines(t)
		if len(lines) != 3 || lines[0]["kind"] != "message" || lines[1]["kind"] != "activity" || lines[2]["kind"] != "sync" {
			t.Fatalf("watch event shape/order changed: %v", lines)
		}
		assertMessageMedia(t, lines[0], 1)
		if lines[0]["item"].(map[string]any)["id"] != nil || lines[1]["media_coverage"] != nil ||
			lines[2]["media_coverage"] != nil {
			t.Fatalf("watch projection/scope changed: %v", lines)
		}
	}
}

func TestMessageMediaCoverageZeroAndMixedSearch(t *testing.T) {
	e := searchMailEnv(t)
	for _, fields := range [][]string{nil, {"--fields", "text"}} {
		for _, test := range []struct {
			args  []string
			count int
		}{
			{[]string{"Hello"}, 2},
			{[]string{"Hello", "--limit", "1"}, 0},
			{[]string{"Hello", "--limit", "2"}, 1},
			{[]string{"no-such-synthetic-media-result", "--source", "chats"}, 0},
		} {
			assertMessageMedia(t, searchJSON(t, e, append(test.args, fields...)...), test.count)
		}
	}
	assertMessageMedia(t, jsonRun(t, e, "messages", "--since", "2099-01-01"), 0)
	for _, args := range [][]string{
		{"search", "Hello", "--source", "mail"},
		{"conversations"},
		{"unread", "--by-conversation"},
		{"activity"},
		{"sql", "select 1 as synthetic_value"},
	} {
		if doc := jsonRun(t, e, args...); doc["media_coverage"] != nil {
			t.Fatalf("%v invented typed-message media scope: %v", args, doc["media_coverage"])
		}
	}
}

func TestMessageMediaCoverageCalendar(t *testing.T) {
	e := calEnv(t)
	review := itemBySubject(t, agenda(t, e, "--from", "2023-11-20", "--days", "1", "--account", tenantA+"/"+userA), "Fixture planning review")
	id := review["event_id"].(string)
	acct := []string{"--account", tenantA + "/" + userA}
	assertMessageMedia(t, eventDoc(t, e, id, acct...), 9)
	projected := eventDoc(t, e, id, append(acct, "--fields", "subject")...)
	assertMessageMedia(t, projected, 9)
	if projected["chat"] != nil {
		t.Fatal("event projection retained the chat just to explain missing media")
	}
	for _, fields := range [][]string{nil, {"--fields", "subject"}} {
		args := append([]string{"--no-sync", "--format", "text", "--account", tenantA + "/" + userA,
			"calendar", "event", id}, fields...)
		code, out, stderr := e.run(args...)
		if code != 0 || !strings.Contains(out, "media bytes not included") ||
			!strings.Contains(out, "reference inspection not requested") {
			t.Fatalf("calendar text omitted recent-message media scope: %d %s %s", code, out, stderr)
		}
	}
	if doc := agenda(t, e, "--from", "2023-11-20", "--days", "1", "--account", tenantA+"/"+userA); doc["media_coverage"] != nil {
		t.Fatal("calendar agenda invented a recent-message media scope")
	}
}

func TestMessageMediaCoverageDoesNotReadHTML(t *testing.T) {
	e := appEnv(t)
	e.exec("alter table messages drop column content_html")
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"messages", "--conversation", appChat},
		{"unread", "--conversation", appChat},
		{"thread", appChat, "app-1"},
		{"search", "evidence", "--source", "chats"},
	} {
		doc := jsonRun(t, e, append(args, "--fields", "text")...)
		count := 2
		if args[0] == "thread" {
			count = 1
		}
		assertMessageMedia(t, doc, count)
	}
	after, err := os.ReadFile(e.db)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("media disclosure changed archive bytes: %v", err)
	}
	code, _, _ := e.run("--no-sync", "--json", "messages", "--conversation", appChat, "--html")
	if code == 0 {
		t.Fatal("HTML fault fixture did not actually prevent body lookup")
	}
}

func TestMessageMediaCoverageLegacyPayload(t *testing.T) {
	e := appEnv(t)
	e.exec(`update messages set content_html='<p>synthetic <strong>body</strong></p>' where id='app-1'`)
	doc := jsonRun(t, e, "messages", "--conversation", appChat, "--html", "--fields", "id,text,html")
	assertMessageMedia(t, doc, 2)
	app := byID(t, items(t, doc), "app-1")
	want := map[string]any{"id": "app-1", "text": "evidence answer", "html": "<p>synthetic <strong>body</strong></p>"}
	if !reflect.DeepEqual(app, want) || doc["count"] != float64(2) || doc["truncated"] != false ||
		doc["implicit_sync"] != "disabled" {
		t.Fatalf("additive media receipt changed a legacy payload: %v / %v", app, doc)
	}
	w, out, _ := jsonWatcher(t, e.root, e.db)
	if err := w.reportErr(context.Canceled); err != nil {
		t.Fatal(err)
	}
	for _, line := range out.lines(t) {
		if line["media_coverage"] != nil {
			t.Fatal("watch error invented media coverage")
		}
	}
}
