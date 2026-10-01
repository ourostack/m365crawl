package cli

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/syncer"
)

func mentionKinds(t *testing.T, its []map[string]any) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, it := range its {
		k, _ := it["mention_kind"].(string)
		out[k]++
	}
	return out
}

func TestMessagesCarryMentionKind(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, stdout, stderr := e.run("--max-age", "0", "messages", "--mentions-me", "--limit", "100")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	// Per account: two person mentions (one by id in a chat, one with a tag beside it), a channel
	// mention (the topic root, known from the feed) and team, tag and everyone broadcasts.
	got := mentionKinds(t, items(t, decode(t, stdout)))
	want := map[string]int{"person": 4, "channel": 2, "team": 2, "tag": 2, "everyone": 2}
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("kind %s = %d, want %d (all %v)", k, got[k], n, got)
		}
	}
	// A message that does not mention the account has no mention_kind key at all.
	_, stdout, _ = e.run("--max-age", "0", "messages", "--limit", "200")
	for _, it := range items(t, decode(t, stdout)) {
		if _, has := it["mention_kind"]; has && it["mentions_me"] != true {
			t.Fatalf("mention_kind on a message that does not mention me: %v", it)
		}
	}
	// It survives --fields.
	_, stdout, _ = e.run("--max-age", "0", "--fields", "id,mentions_me,mention_kind", "messages", "--mentions-me", "--limit", "1")
	if it := items(t, decode(t, stdout))[0]; it["mention_kind"] == nil {
		t.Fatalf("--fields dropped mention_kind: %v", it)
	}
}

func TestDirectMentionsFlag(t *testing.T) {
	e := newEnv(t)
	e.sync()
	for _, args := range [][]string{{"messages"}, {"search", "see this"}, {"search"}} {
		code, stdout, stderr := e.run(append([]string{"--max-age", "0"}, append(args, "--direct-mentions")...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, stderr)
		}
		its := items(t, decode(t, stdout))
		if len(its) == 0 {
			t.Fatalf("%v: no items", args)
		}
		for _, it := range its {
			if it["mention_kind"] != "person" {
				t.Errorf("%v: not a direct mention: %v", args, it["mention_kind"])
			}
		}
	}
	_, stdout, _ := e.run("--max-age", "0", "messages", "--mentions-me", "--direct-mentions", "--limit", "100")
	if n := len(items(t, decode(t, stdout))); n != 4 {
		t.Errorf("direct mentions of me across both accounts = %d, want 4", n)
	}
	// activity: only mention items whose subtype is person.
	_, stdout, _ = e.run("--max-age", "0", "activity", "--direct-mentions", "--limit", "100")
	acts := items(t, decode(t, stdout))
	if len(acts) != 2 {
		t.Fatalf("direct activity = %d, want 2", len(acts))
	}
	for _, it := range acts {
		if it["type"] != "mentionInChat" || it["subtype"] != "person" {
			t.Errorf("activity --direct-mentions returned %v", it)
		}
	}
}

func TestActivityActors(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "--account", tenantA+"/"+userA, "activity", "--limit", "100")
	byType := map[string]map[string]any{}
	for _, it := range items(t, decode(t, stdout)) {
		sub, _ := it["subtype"].(string)
		byType[it["type"].(string)+"/"+sub] = it
	}
	pat, patID := "Pat Example", "8:orgid:00000000-0000-4000-8000-0000000000ee"
	// A reaction names who reacted. The reacted-to message's sender is Pat; the like came from Pat
	// (the account's own heart never reaches its feed), and the sender stays the message author.
	react := byType["reactionInChat/like"]
	if react["actor_inferred"] != true {
		t.Errorf("a reaction actor is inferred: %v", react)
	}
	if mention := byType["mention/channel"]; mention["actor_inferred"] != nil {
		t.Errorf("an exact actor must not say inferred: %v", mention)
	}
	if react["actor_id"] != patID || react["actor_name"] != pat {
		t.Errorf("reaction actor = %v / %v", react["actor_id"], react["actor_name"])
	}
	for _, k := range []string{"mentionInChat/person", "mention/channel", "reply/channel", "replyToReply/channel", "follow/"} {
		it := byType[k]
		if it == nil {
			t.Fatalf("no %s item in %v", k, byType)
		}
		if it["actor_id"] != it["sender_id"] || it["actor_name"] != it["sender_name"] || it["actor_id"] == "" {
			t.Errorf("%s: actor %v/%v, sender %v/%v", k, it["actor_id"], it["actor_name"], it["sender_id"], it["sender_name"])
		}
	}
	// No data names an actor: omitted, not blank.
	for _, k := range []string{"msGraph/approvalCompleted", "reaction/heart"} {
		it := byType[k]
		if it == nil {
			t.Fatalf("no %s item", k)
		}
		if _, has := it["actor_id"]; has {
			t.Errorf("%s has an actor: %v", k, it)
		}
		if _, has := it["actor_name"]; has {
			t.Errorf("%s has an actor name: %v", k, it)
		}
	}
	_, stdout, _ = e.run("--max-age", "0", "--fields", "id,actor_name", "activity", "--limit", "1")
	if len(items(t, decode(t, stdout))) != 1 {
		t.Fatal("--fields actor_name must be accepted")
	}
}

func TestTeamsCommand(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, stdout, stderr := e.run("--max-age", "0", "teams")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m := decode(t, stdout)
	its := items(t, m)
	if len(its) != 2 || m["count"] != float64(2) || m["truncated"] != false {
		t.Fatalf("teams = %v", m)
	}
	for _, it := range its {
		for _, k := range []string{"tenant_id", "user_id", "team_id", "display_name", "channel_count", "last_activity_at", "unread_count"} {
			if _, has := it[k]; !has {
				t.Errorf("team item lacks %s: %v", k, it)
			}
		}
		if !strings.HasPrefix(it["display_name"].(string), "Fixture team ") || it["channel_count"] != float64(2) {
			t.Errorf("team = %v", it)
		}
	}
	_, stdout, _ = e.run("--max-age", "0", "--account", tenantA+"/"+userA, "teams", "--fields", "team_id,unread_count")
	one := items(t, decode(t, stdout))
	if len(one) != 1 || len(one[0]) != 2 {
		t.Fatalf("--account/--fields: %v", one)
	}
	_, stdout, _ = e.run("--max-age", "0", "teams", "--limit", "1")
	cut := decode(t, stdout)
	if len(items(t, cut)) != 1 || cut["truncated"] != true || cut["total"] != float64(2) {
		t.Fatalf("--limit: %v", cut)
	}
	// A team_id from the list works as --team.
	id := one[0]["team_id"].(string)
	if code, _, stderr := e.run("--max-age", "0", "conversations", "--team", id); code != 0 {
		t.Fatalf("--team %s: %s", id, stderr)
	}
	for _, args := range [][]string{{"teams", "--limit", "0"}, {"teams", "--fields", "nope"}} {
		if code, _, _ := e.run(append([]string{"--max-age", "0"}, args...)...); code != errs.ExitUsage {
			t.Errorf("%v: exit %d, want usage", args, code)
		}
	}
	// Text mode.
	code, stdout, _ = e.run("--max-age", "0", "--format", "text", "teams")
	if code != 0 || !strings.Contains(stdout, "Fixture team 1") || !strings.Contains(stdout, "channels") {
		t.Fatalf("text: %d %s", code, stdout)
	}
	// Nothing archived yet.
	fresh := newEnv(t)
	_, stdout, _ = fresh.run("--max-age", "0", "teams")
	if m := decode(t, stdout); m["count"] != float64(0) || m["needs_sync"] != true {
		t.Fatalf("empty archive: %v", m)
	}
}

func TestUnknownTeamErrorPointsAtTeams(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, _, stderr := e.run("--max-age", "0", "messages", "--team", "no such team")
	if code != errs.ExitUsage || !strings.Contains(errorOf(t, stderr)["fix"].(string), "teamscrawl teams") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestUnreadSince(t *testing.T) {
	e := newEnv(t)
	e.sync()
	total := func(args ...string) float64 {
		t.Helper()
		code, stdout, stderr := e.run(append([]string{"--max-age", "0", "unread", "--limit", "1"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, stderr)
		}
		m := decode(t, stdout)
		if tot, ok := m["total"].(float64); ok {
			return tot
		}
		return m["count"].(float64)
	}
	all := total()
	if all < 4 {
		t.Fatalf("fixture unread = %v", all)
	}
	// The fixture's messages are from November 2023: nothing is newer than a week, everything is
	// newer than 2023-01-01, and an absolute cutoff in the middle splits the unread messages.
	if n := total("--since", "7d"); n != 0 {
		t.Errorf("--since 7d = %v", n)
	}
	if n := total("--since", "2023-01-01"); n != all {
		t.Errorf("--since 2023-01-01 = %v, want %v", n, all)
	}
	mid := total("--since", "2023-11-14T22:14:00Z")
	if mid <= 0 || mid >= all {
		t.Errorf("--since mid = %v (all %v)", mid, all)
	}
	// --by-conversation, --team and --include-channels honor the same cutoff.
	_, stdout, _ := e.run("--max-age", "0", "unread", "--by-conversation", "--since", "7d")
	if m := decode(t, stdout); m["count"] != float64(0) {
		t.Errorf("by-conversation since 7d = %v", m)
	}
	_, stdout, _ = e.run("--max-age", "0", "unread", "--by-conversation", "--include-channels", "--team", "fixture team 1", "--since", "2023-01-01")
	if m := decode(t, stdout); m["count"].(float64) < 1 {
		t.Errorf("by-conversation team since = %v", m)
	}
	if code, _, _ := e.run("--max-age", "0", "unread", "--since", "yesterday-ish"); code != errs.ExitUsage {
		t.Errorf("bad --since: exit %d", code)
	}
}

var notice = regexp.MustCompile(`(?m)^teamscrawl: syncing — (.+)$`)

func TestImplicitSyncNotice(t *testing.T) {
	e := newEnv(t)
	// Never synced: the notice says so, and the result says what the sync did.
	code, stdout, stderr := e.run("messages", "--limit", "1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if n := decode(t, stderr); n["notice"] != "syncing" || n["reason"] != "never_synced" || n["max_age_seconds"] != float64(900) || len(n) != 3 {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stdout, "syncing") {
		t.Fatalf("the notice leaked to stdout: %s", stdout)
	}
	res := decode(t, stdout)
	synced, _ := res["synced"].(map[string]any)
	if synced["status"] != "ok" {
		t.Fatalf("synced = %v", res["synced"])
	}
	if secs, ok := synced["seconds"].(float64); !ok || secs < 0 || secs > 120 {
		t.Fatalf("synced.seconds = %v", synced["seconds"])
	}
	if res["archive_age_seconds"] == nil {
		t.Fatalf("archive_age_seconds missing: %v", res)
	}
	// Fresh archive: no sync, no notice, no synced key.
	_, stdout, stderr = e.run("messages", "--limit", "1")
	if stderr != "" || decode(t, stdout)["synced"] != nil {
		t.Fatalf("fresh read: stderr %q, stdout %s", stderr, stdout)
	}
	// --max-age 0 never syncs.
	if _, _, stderr = e.run("--max-age", "0", "messages"); stderr != "" {
		t.Fatalf("--max-age 0: %q", stderr)
	}
	// Stale: the notice gives the age and the limit.
	e.exec(`update sync_runs set finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-134 minutes','-30 seconds'), started_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-134 minutes','-31 seconds')`)
	_, stdout, stderr = e.run("messages", "--limit", "1")
	if n := decode(t, stderr); n["notice"] != "syncing" || n["reason"] != "stale" || n["max_age_seconds"] != float64(900) || n["archive_age_seconds"].(float64) < 8040 || n["archive_age_seconds"].(float64) > 8100 {
		t.Fatalf("stale stderr = %q", stderr)
	}
	if s, _ := decode(t, stdout)["synced"].(map[string]any); s["status"] == nil {
		t.Fatalf("stale synced = %v", s)
	}
	// Text mode prints the same line on stderr.
	e.exec(`update sync_runs set finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-90 seconds'), started_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-91 seconds')`)
	_, stdout, stderr = e.run("--format", "text", "--max-age", "1m", "messages", "--limit", "1")
	if m := notice.FindStringSubmatch(stderr); m == nil || m[1] != "archive is 1m old (max-age 1m)" {
		t.Fatalf("text stderr = %q", stderr)
	}
	if strings.Contains(stdout, "teamscrawl: syncing") {
		t.Fatalf("text stdout: %s", stdout)
	}
}

func TestImplicitSyncNoticeFormats(t *testing.T) {
	for _, tc := range []struct {
		age, max time.Duration
		want     string
	}{
		{45 * time.Second, 30 * time.Second, "archive is 45s old (max-age 30s)"},
		{2*time.Hour + 14*time.Minute + 59*time.Second, 15 * time.Minute, "archive is 2h14m old (max-age 15m)"},
		{3 * time.Hour, 90 * time.Minute, "archive is 3h old (max-age 1h30m)"},
		{26 * time.Hour, 24 * time.Hour, "archive is 26h old (max-age 24h)"},
		{0, 15 * time.Minute, "no complete sync yet (max-age 15m)"},
		{500 * time.Millisecond, time.Millisecond, "archive is 0s old (max-age 1ms)"},
	} {
		if got := syncNotice(tc.age, tc.max); got != "teamscrawl: syncing — "+tc.want {
			t.Errorf("syncNotice(%v, %v) = %q", tc.age, tc.max, got)
		}
	}
}

func TestImplicitSyncStatuses(t *testing.T) {
	old := runSync
	t.Cleanup(func() { runSync = old })
	for _, tc := range []struct {
		name   string
		rep    syncer.Report
		err    error
		status string
		code   string
	}{
		{"failed", syncer.Report{}, errors.New("disk on fire"), "failed", errs.CodeInternal},
		{"partial", syncer.Report{Status: syncer.StatusPartial}, errs.PartialSync("one source failed"), "partial", errs.CodePartialSync},
	} {
		runSync = func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
			return tc.rep, nil, tc.err
		}
		e := newEnv(t)
		code, stdout, stderr := e.run("search", "x")
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", tc.name, code, stderr)
		}
		res := decode(t, stdout)
		synced, _ := res["synced"].(map[string]any)
		if synced["status"] != tc.status || res["sync_error"] == nil || !strings.Contains(stderr, `"notice":"syncing"`) {
			t.Errorf("%s: synced %v, sync_error %v, stderr %q", tc.name, res["synced"], res["sync_error"], stderr)
		}
	}
}

func TestWhoamiCarriesSyncedInBothPlaces(t *testing.T) {
	e := newEnv(t)
	_, stdout, _ := e.run("whoami")
	res := decode(t, stdout)
	archive, _ := res["archive"].(map[string]any)
	if res["synced"] == nil || archive["synced"] == nil {
		t.Fatalf("whoami synced: %v", res)
	}
}

func TestImplicitSyncNoticeIsJSONInLogMode(t *testing.T) {
	e := newEnv(t)
	_, _, stderr := e.run("--format", "log", "messages", "--limit", "1")
	if n := decode(t, stderr); n["notice"] != "syncing" || n["reason"] != "never_synced" {
		t.Fatalf("stderr = %q", stderr)
	}
}
