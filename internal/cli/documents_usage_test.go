package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/onedriveusage"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

var usageCLIAt = time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)

// seedUsageCLI admits one synthetic usage capture, or records a first failed attempt when the
// result is empty.
func seedUsageCLI(t *testing.T, e *env, locator string, result onedriveusage.Result) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	attempt, err := st.BeginUsageDocumentAttempt(ctx, store.KnowledgeInput{Kind: store.UsageDocumentKind,
		Platform: "synthetic", Locator: locator, At: usageCLIAt})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Recent)+len(result.Documents)+len(result.Collaborators) == 0 {
		err = st.RecordKnowledgeFailure(ctx, attempt, store.KnowledgeOutcome{Status: "failed",
			Reason: "onedrive_usage_source_busy", At: usageCLIAt})
	} else {
		status := "complete"
		if len(result.Losses) > 0 {
			status = "partial"
		}
		err = st.CommitUsageDocumentObservations(ctx, attempt, store.KnowledgeOutcome{Status: status,
			ContentDigest: "synthetic-usage", Qualification: store.UsageDocumentQualification, At: usageCLIAt},
			result, store.UsageCaptureMetadata{AcquiredAt: &usageCLIAt, SourceObservedAt: &usageCLIAt})
	}
	if err != nil {
		t.Fatal(err)
	}
}

func usageCLIDocument(id, title string, history ...onedriveusage.Share) onedriveusage.DocumentObservation {
	return onedriveusage.DocumentObservation{ID: id, Format: "variant-a", Title: title,
		URL: "https://example.invalid/sites/synthetic/" + id + ".docx", Extension: "docx", Owner: "Synthetic Owner",
		CreatedRaw: "2026-10-01T08:00:00Z", ModifiedRaw: "2026-10-09T14:34:56.5+02:00",
		SiteID: "site-" + id, WebID: "web-" + id, ListID: "list-" + id, UniqueID: "unique-" + id, History: history}
}

func usageCLIRecent(id string) onedriveusage.RecentObservation {
	return onedriveusage.RecentObservation{ID: id, ItemID: "item-" + id, ListID: "list-" + id, WebID: "web-" + id, SiteID: "site-" + id,
		Type: "Word", Source: "native-source", Extension: "docx", WorkingSetID: "working-" + id,
		LastModifiedRaw: "2026-10-10T10:00:00Z", LastAccessedRaw: "2026-10-11T11:00:00Z", FileModifiedRaw: "2026-10-09T12:34:56Z",
		LastSharedRaw: "2026-10-08T09:00:00Z"}
}

func usageCLIShare(name string, participants int) onedriveusage.Share {
	share := onedriveusage.Share{DisplayName: name, SMTP: "sharer@example.invalid", AtRaw: "2026-10-08T09:00:00Z",
		ConversationID: "19:synthetic-thread@thread.v2", Subject: "Synthetic subject"}
	for i := range participants {
		share.Participants = append(share.Participants, onedriveusage.Person{DisplayName: "Participant " + strconv.Itoa(i),
			SMTP: "participant" + strconv.Itoa(i) + "@example.invalid"})
	}
	return share
}

// usageCLIItem returns the one usage item whose native record has this type and ID.
func usageCLIItem(t *testing.T, rows []map[string]any, kind, id string) map[string]any {
	t.Helper()
	for _, row := range rows {
		native, _ := row["usage_native"].(map[string]any)
		payload, _ := native[strings.ToUpper(kind[:1])+kind[1:]].(map[string]any)
		if native["Type"] == kind && payload["ID"] == id {
			return row
		}
	}
	t.Fatalf("no usage %s row %s in %v", kind, id, rows)
	return nil
}

func usageCLIRun(t *testing.T, e *env, args ...string) (map[string]any, string) {
	t.Helper()
	return decode(t, usageCLIText(t, e, args...)), ""
}

// usageCLIText runs an archive-only command that must succeed and returns its output.
func usageCLIText(t *testing.T, e *env, args ...string) string {
	t.Helper()
	code, stdout, stderr := e.run(append([]string{"--max-age", "0"}, args...)...)
	if code != 0 {
		t.Fatalf("%v: exit %d %s", args, code, stderr)
	}
	return stdout
}

// Review focus 4: a preview hides a later participant, and field and global limits remove every
// usage hit. The explicit counts say more exists, and the coverage of every usage source stays.
func TestUsagePreviewAndZeroHitCoverage(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	stated := int64(7)
	wide := usageCLIShare("First sharer", 101)
	wide.Participants[100] = onedriveusage.Person{DisplayName: "Hiddenlater Participant", SMTP: "hiddenlater@example.invalid"}
	wide.Participants[3] = wide.Participants[2] // a repeated native row stays a row
	wide.ParticipantsCount = &stated
	history := []onedriveusage.Share{wide}
	for i := 1; i <= 100; i++ {
		history = append(history, usageCLIShare("Sharer "+strconv.Itoa(i), 0))
	}
	history[100].DisplayName = "Hiddenlast Sharer"
	over := usageCLIDocument("native-over", "Quarterly usage over", history...)
	latest := usageCLIShare("Latest sharer", 101)
	over.Latest = &latest
	exact := usageCLIDocument("native-exact", "Quarterly usage exact", append([]onedriveusage.Share{usageCLIShare("Exact sharer", 100)}, history[1:100]...)...)
	none := usageCLIDocument("native-none", "Quarterly usage none")
	seedUsageCLI(t, e, "/fictional/Business_one/Microsoft.FileUsageSync.db", onedriveusage.Result{
		Recent:        []onedriveusage.RecentObservation{usageCLIRecent("native-over"), usageCLIRecent("native-exact"), usageCLIRecent("native-none")},
		Documents:     []onedriveusage.DocumentObservation{over, exact, none},
		Collaborators: []onedriveusage.Collaborator{{ID: "person-1", DisplayName: "Synthetic Collaborator", Emails: []string{"collaborator@example.invalid"}}},
		Losses:        []onedriveusage.Loss{{Code: "onedrive_usage_document_unmapped", Count: 2}}})
	seedUsageCLI(t, e, "/fictional/Business_two/Microsoft.FileUsageSync.db", onedriveusage.Result{})

	doc, _ := usageCLIRun(t, e, "docs", "list", "--application", "onedrive", "--limit", "0")
	rows := items(t, doc)
	if len(rows) != 7 {
		t.Fatalf("%d usage rows, want 3 recent, 3 documents and 1 collaborator", len(rows))
	}
	id := usageCLIItem(t, rows, "document", "native-over")["id"].(string)
	stdout := usageCLIText(t, e, "docs", "show", id)
	doc = decode(t, stdout)
	detail := items(t, doc)[0]
	native := detail["usage_native"].(map[string]any)
	document := native["Document"].(map[string]any)
	shown := document["History"].([]any)
	if len(shown) != 100 || document["HistoryCaptured"] != float64(101) || document["HistoryTruncated"] != true ||
		document["ParticipantsCaptured"] != float64(101) || native["PreviewTruncated"] != true {
		t.Fatalf("history preview: %d shown, captured %v, truncated %v", len(shown), document["HistoryCaptured"], document["HistoryTruncated"])
	}
	for i, raw := range shown {
		if raw.(map[string]any)["Position"] != float64(i) {
			t.Fatalf("history instance %d lost its native position: %v", i, raw.(map[string]any)["Position"])
		}
	}
	first := shown[0].(map[string]any)
	participants := first["Participants"].([]any)
	if len(participants) != 100 || first["ParticipantsCaptured"] != float64(101) || first["ParticipantsTruncated"] != true ||
		first["ParticipantsCount"] != float64(7) || participants[2].(map[string]any)["SMTP"] != participants[3].(map[string]any)["SMTP"] {
		t.Fatalf("participant preview: %d shown, captured %v, stated %v", len(participants), first["ParticipantsCaptured"], first["ParticipantsCount"])
	}
	if count, present := shown[1].(map[string]any)["ParticipantsCount"]; !present || count != nil ||
		len(shown[1].(map[string]any)["Participants"].([]any)) != 0 || shown[1].(map[string]any)["ParticipantsCaptured"] != float64(0) {
		t.Fatalf("an instance with no stated count and no participants: %v", shown[1])
	}
	last := document["Latest"].(map[string]any)
	if len(last["Participants"].([]any)) != 100 || last["ParticipantsCaptured"] != float64(101) || last["ParticipantsTruncated"] != true || last["Position"] != nil {
		t.Fatalf("latest sharing claim is not bounded like a history instance: %v", last["ParticipantsCaptured"])
	}
	if strings.Contains(stdout, "Hiddenlater") || strings.Contains(stdout, "Hiddenlast") {
		t.Fatal("a participant or instance past the preview was printed")
	}
	// What the preview hides is still archived, searchable and reachable by the printed SQL.
	for _, query := range []string{"Hiddenlater", "Hiddenlast"} {
		found, _ := usageCLIRun(t, e, "docs", "search", query)
		if hits := items(t, found); len(hits) != 1 || hits[0]["id"] != id {
			t.Fatalf("search %q did not find the row whose preview hides it", query)
		}
	}
	inspect, _ := detail["full_inspection"].(string)
	rowid := strings.TrimPrefix(id, "document-observation:")
	want := `m365crawl --db ` + quoteArg(e.db) + ` --max-age 0 sql "select usage_json from document_observations where rowid=` + rowid + `" --limit 1`
	if inspect != want {
		t.Fatalf("full inspection = %q, want %q", inspect, want)
	}
	code, full, stderr := e.run("--max-age", "0", "--max-text", "0", "sql", "select usage_json from document_observations where rowid="+rowid, "--limit", "1")
	if code != 0 || !strings.Contains(full, "Hiddenlater Participant") || !strings.Contains(full, "Hiddenlast Sharer") {
		t.Fatalf("the printed SQL does not return the complete payload: %d %s", code, stderr)
	}
	// Exactly 100 is not truncated; none is an empty list, not null.
	doc, _ = usageCLIRun(t, e, "docs", "show", usageCLIItem(t, rows, "document", "native-exact")["id"].(string))
	native = items(t, doc)[0]["usage_native"].(map[string]any)
	document = native["Document"].(map[string]any)
	first = document["History"].([]any)[0].(map[string]any)
	if len(document["History"].([]any)) != 100 || document["HistoryCaptured"] != float64(100) || document["HistoryTruncated"] != false ||
		len(first["Participants"].([]any)) != 100 || first["ParticipantsTruncated"] != false || native["PreviewTruncated"] != false || document["Latest"] != nil {
		t.Fatalf("a preview of exactly 100 is marked truncated: %v %v", document["HistoryTruncated"], first["ParticipantsTruncated"])
	}
	doc, _ = usageCLIRun(t, e, "docs", "show", usageCLIItem(t, rows, "document", "native-none")["id"].(string))
	document = items(t, doc)[0]["usage_native"].(map[string]any)["Document"].(map[string]any)
	if history, ok := document["History"].([]any); !ok || len(history) != 0 || document["HistoryCaptured"] != float64(0) || document["HistoryTruncated"] != false {
		t.Fatalf("a document with no sharing history: %v", document["History"])
	}
	// Field and global limits remove every usage hit: both usage sources stay in the coverage,
	// with the partial loss and the failed source's retry.
	check := func(args []string, wantItems int, wantTruncated bool) {
		t.Helper()
		stdout := usageCLIText(t, e, args...)
		doc := decode(t, stdout)
		rows := items(t, doc)
		for _, row := range rows {
			if row["variant"] != nil || row["usage_native"] != nil {
				t.Fatalf("%v: a usage row survived the limit or filter: %v", args, row)
			}
		}
		if len(rows) != wantItems || doc["truncated"] != wantTruncated {
			t.Fatalf("%v: items %d truncated %v", args, len(rows), doc["truncated"])
		}
		usage, partial, failed := 0, false, false
		for _, source := range coverageSources(t, doc) {
			if source["kind"] != store.UsageDocumentKind {
				continue
			}
			usage++
			fix, _ := source["fix"].(string)
			if source["status"] == "failed" {
				failed = source["source_id"] == nil && source["reason"] == "onedrive_usage_source_busy" && strings.Contains(fix, "--onedrive-usage-file")
			} else {
				partial = source["coverage"] == "partial" && source["losses"].(map[string]any)["onedrive_usage_document_unmapped"] == float64(2)
			}
		}
		if usage != 2 || !partial || !failed || !strings.Contains(stdout, "onedrive_usage_document_unmapped:") {
			t.Fatalf("%v: usage coverage hidden behind zero usage hits: %s", args, stdout)
		}
	}
	check([]string{"docs", "list", "--limit", "1"}, 1, true)
	check([]string{"docs", "search", "Quarterly", "--limit", "1"}, 1, true)
	check([]string{"search", "Quarterly", "--source", "documents", "--limit", "1"}, 1, true)
	check([]string{"search", "Quarterly", "--limit", "1"}, 1, true)
	check([]string{"docs", "list", "--surface", "shared"}, 1, false)
	check([]string{"docs", "list", "--application", "supplied"}, 1, false)
	check([]string{"docs", "list", "--since", "2026-10-10"}, 1, false)
	check([]string{"docs", "search", "nothing-matches-this"}, 0, false)
	// A projection that drops every usage field keeps the rows, their count and the coverage.
	doc, _ = usageCLIRun(t, e, "--fields", "id", "docs", "list", "--application", "onedrive", "--limit", "2")
	if rows := items(t, doc); len(rows) != 2 || len(rows[0]) != 1 || doc["truncated"] != true || len(coverageSources(t, doc)) != 3 {
		t.Fatalf("projected usage rows: %v", rows)
	}
	// Descriptive text is clipped once and stays clipped the same way; identifiers never are.
	clipped := usageCLIText(t, e, "--max-text", "5", "docs", "show", id)
	again := usageCLIText(t, e, "--max-text", "5", "docs", "show", id)
	short := items(t, decode(t, clipped))[0]
	document = short["usage_native"].(map[string]any)["Document"].(map[string]any)
	first = document["History"].([]any)[0].(map[string]any)
	if clipped != again || short["text_truncated"] != true || short["title"] != "Quar…" || document["Title"] != "Quar…" || document["Owner"] != "Synt…" ||
		first["DisplayName"] != "Firs…" || first["Subject"] != "Synt…" || first["Participants"].([]any)[0].(map[string]any)["DisplayName"] != "Part…" ||
		document["ID"] != "native-over" || document["URL"] != over.URL || short["url"] != over.URL || document["UniqueID"] != "unique-native-over" ||
		first["SMTP"] != "sharer@example.invalid" || first["ConversationID"] != "19:synthetic-thread@thread.v2" ||
		first["At"].(map[string]any)["Raw"] != "2026-10-08T09:00:00Z" {
		t.Fatalf("clipping changed an identifier or was not stable: %v", document["Title"])
	}
	if reclipped, cut := truncateRunes(short["title"].(string), 5); reclipped != short["title"] || cut {
		t.Fatalf("clipping a clipped value changed it: %q", reclipped)
	}
}

// Review focus 5: a literal thread ID and person name in a sharing claim equal another account's
// archived conversation and person. No runnable thread or person follow-up and no alias appears.
func TestUsageUnresolvedFollowups(t *testing.T) {
	e := newEnv(t)
	e.sync()
	scalar := func(query string) string {
		t.Helper()
		doc, _ := usageCLIRun(t, e, "sql", query)
		rows, _ := doc["rows"].([]any)
		if len(rows) == 0 {
			return ""
		}
		value, _ := rows[0].([]any)[0].(string)
		return value
	}
	conversation := scalar("select id from conversations order by id limit 1")
	person := scalar("select display_name from people where display_name<>'' order by id limit 1")
	personID := scalar("select id from people where display_name<>'' order by id limit 1")
	if conversation == "" || person == "" {
		t.Fatal("the fixture archive has no conversation or person to collide with")
	}
	counts := func() string {
		t.Helper()
		return scalar("select (select count(*) from people)||'/'||(select count(*) from people_observations)||'/'||(select count(*) from conversations) as n")
	}
	before := counts()
	share := onedriveusage.Share{DisplayName: person, SMTP: "collision@example.invalid", AadID: personID, AtRaw: "2026-10-08T09:00:00Z",
		ConversationID: conversation, Subject: "Synthetic subject",
		Participants: []onedriveusage.Person{{DisplayName: person, SMTP: "collision@example.invalid"}}}
	document := usageCLIDocument("native-1", "Collision usage document", share)
	document.Latest = &share
	seedUsageCLI(t, e, "/fictional/Business_one/Microsoft.FileUsageSync.db", onedriveusage.Result{
		Recent:        []onedriveusage.RecentObservation{usageCLIRecent("native-1")},
		Documents:     []onedriveusage.DocumentObservation{document},
		Collaborators: []onedriveusage.Collaborator{{ID: "person-1", UserID: personID, DisplayName: person, Emails: []string{"collision@example.invalid"}}}})
	if after := counts(); after != before {
		t.Fatalf("a usage capture created or changed people, people observations or conversations: %s -> %s", before, after)
	}
	doc, _ := usageCLIRun(t, e, "docs", "list", "--application", "onedrive")
	rows := items(t, doc)
	id := usageCLIItem(t, rows, "document", "native-1")["id"].(string)
	rowid := strings.TrimPrefix(id, "document-observation:")
	allowed := `m365crawl --db ` + quoteArg(e.db) + ` --max-age 0 sql "select usage_json from document_observations where rowid=`
	for _, args := range [][]string{{"docs", "list", "--application", "onedrive"}, {"docs", "show", id}, {"docs", "search", "Collision"},
		{"search", "Collision", "--source", "documents"}, {"search", "Collision"},
		{"docs", "show", usageCLIItem(t, rows, "recent", "native-1")["id"].(string)},
		{"docs", "show", usageCLIItem(t, rows, "collaborator", "person-1")["id"].(string)},
		{"--format", "text", "docs", "show", id}, {"--format", "text", "docs", "list", "--application", "onedrive"},
		{"--format", "text", "search", "Collision"}} {
		code, stdout, stderr := e.run(append([]string{"--max-age", "0"}, args...)...)
		if code != 0 || !strings.Contains(stdout, "Collision") && !strings.Contains(stdout, "native-1") && !strings.Contains(stdout, "person-1") {
			t.Fatalf("%v: %d %s", args, code, stderr)
		}
		// Every printed command is the row's own archive-only SQL inspection or the documented
		// docs show route; none names the claimed thread, person or address. JSON output is
		// decoded first: a Windows archive path is escaped inside a JSON string.
		for _, printed := range printedStrings(stdout) {
			for rest := printed; ; {
				at := strings.Index(rest, "m365crawl ")
				if at < 0 {
					break
				}
				command, _, _ := strings.Cut(rest[at:], "\n")
				if !strings.HasPrefix(command, allowed) && !strings.HasPrefix(command, "m365crawl has not linked the row") {
					t.Fatalf("%v printed a follow-up that is not the row's own SQL inspection: %s", args, command)
				}
				rest = rest[at+len("m365crawl "):]
			}
		}
		for _, runnable := range []string{"thread " + conversation, "messages --chat", "chats ", "people show", "person:", "--chat " + conversation,
			"--author", "mail list --from", "alias"} {
			if strings.Contains(stdout, runnable) {
				t.Fatalf("%v printed a thread, person or alias follow-up %q", args, runnable)
			}
		}
		if strings.HasPrefix(args[0], "docs") && args[len(args)-1] != "onedrive" && !strings.Contains(stdout, "unresolved") {
			t.Fatalf("%v does not say the association is unresolved", args)
		}
	}
	// The claims are kept exactly as stated, as claims.
	doc, _ = usageCLIRun(t, e, "docs", "show", id)
	detail := items(t, doc)[0]
	stated := detail["usage_native"].(map[string]any)["Document"].(map[string]any)["History"].([]any)[0].(map[string]any)
	if stated["ConversationID"] != conversation || stated["DisplayName"] != person || stated["AadID"] != personID ||
		detail["association_state"] != "unresolved" || !strings.HasSuffix(detail["full_inspection"].(string), "rowid="+rowid+`" --limit 1`) {
		t.Fatalf("stated claims or the unresolved state changed: %v", stated)
	}
	// A wrong ID is an error that says where real IDs come from; a native ID is never accepted.
	for _, wrong := range []string{"document-observation:99999", "native-1", conversation, "document-observation:0", "document-observation:" + rowid + " or 1=1"} {
		code, stdout, stderr := e.run("--max-age", "0", "docs", "show", wrong)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "docs list") {
			t.Fatalf("docs show %q: exit %d %s", wrong, code, stderr)
		}
	}
	if code, _, stderr := e.run("--max-age", "0", "docs", "show", id); code != 0 {
		t.Fatalf("a correct ID after a wrong one: %s", stderr)
	}
	// Account, author and message-only filters cannot qualify unresolved usage rows.
	for _, args := range [][]string{{"--account", "any", "docs", "list"}, {"search", "Collision", "--source", "documents", "--from", person}} {
		if code, stdout, stderr := e.run(append([]string{"--max-age", "0"}, args...)...); code != 2 || stdout != "" || strings.Contains(stderr, "unknown flag") ||
			!strings.Contains(stderr, "account") {
			t.Fatalf("%v accepted an ownership filter for unresolved documents: %d %s", args, code, stderr)
		}
	}
	stdout := usageCLIText(t, e, "search", "Collision", "--from", person)
	if len(items(t, decode(t, stdout))) != 0 || !strings.Contains(stdout, "document metadata was not searched") {
		t.Fatalf("an author filter matched or silently skipped unresolved usage rows: %s", stdout)
	}
}

func TestUsageCLIReadOnlyAndClocks(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	clocks := usageCLIDocument("native-clock", "Clock usage document", onedriveusage.Share{AtRaw: "last Tuesday", MeetingStartRaw: "2026-10-07T08:00:00-07:00"})
	clocks.CreatedRaw = "yesterday afternoon"
	blank := usageCLIRecent("native-blank")
	blank.FileModifiedRaw, blank.SavedRaw = "", "2026-10-11T11:00:00"
	seedUsageCLI(t, e, "/fictional/Business_one/Microsoft.FileUsageSync.db", onedriveusage.Result{
		Recent:        []onedriveusage.RecentObservation{usageCLIRecent("native-clock"), blank},
		Documents:     []onedriveusage.DocumentObservation{clocks},
		Collaborators: []onedriveusage.Collaborator{{ID: "person-1", DisplayName: "Synthetic Collaborator"}}})
	seedUsageCLI(t, e, "/fictional/Business_two/Microsoft.FileUsageSync.db", onedriveusage.Result{})
	doc, _ := usageCLIRun(t, e, "docs", "list", "--application", "ONEDRIVE")
	rows := items(t, doc)
	if len(rows) != 4 {
		t.Fatalf("%d usage rows", len(rows))
	}
	for _, row := range rows {
		_, native := row["native"]
		_, registry := row["registry_native"]
		_, opened := row["opened_at"]
		if row["variant"] != "onedrive_usage" || row["application"] != "onedrive" || row["surface"] != "" || native || registry || opened ||
			row["text_state"] != "not_captured" || row["association_state"] != "unresolved" || row["captured_at"] != "2026-10-11T03:00:00Z" ||
			!strings.Contains(row["full_inspection"].(string), "select usage_json from document_observations where rowid=") {
			t.Fatalf("usage row shape: %v", row)
		}
	}
	document := usageCLIItem(t, rows, "document", "native-clock")
	claims := document["usage_native"].(map[string]any)["Document"].(map[string]any)
	share := claims["History"].([]any)[0].(map[string]any)
	if document["modified_at"] != "2026-10-09T12:34:56.5Z" || document["title"] != "Clock usage document" || document["url"] != clocks.URL ||
		claims["Modified"].(map[string]any)["Raw"] != "2026-10-09T14:34:56.5+02:00" || claims["Modified"].(map[string]any)["Value"] != "2026-10-09T12:34:56.5Z" ||
		claims["Created"].(map[string]any)["Raw"] != "yesterday afternoon" || claims["Created"].(map[string]any)["Value"] != nil ||
		share["At"].(map[string]any)["Raw"] != "last Tuesday" || share["At"].(map[string]any)["Value"] != nil ||
		share["MeetingStart"].(map[string]any)["Value"] != "2026-10-07T15:00:00Z" {
		t.Fatalf("document clocks: %v", claims)
	}
	recent := usageCLIItem(t, rows, "recent", "native-clock")
	stated := recent["usage_native"].(map[string]any)["Recent"].(map[string]any)
	if recent["modified_at"] != "2026-10-09T12:34:56Z" || recent["title"] != "" || recent["url"] != "" ||
		stated["LastAccessed"].(map[string]any)["Value"] != "2026-10-11T11:00:00Z" || stated["LastShared"].(map[string]any)["Value"] != "2026-10-08T09:00:00Z" ||
		stated["Saved"].(map[string]any)["Raw"] != "" || stated["Saved"].(map[string]any)["Value"] != nil {
		t.Fatalf("recent clocks: %v", stated)
	}
	unknown := usageCLIItem(t, rows, "recent", "native-blank")
	stated = unknown["usage_native"].(map[string]any)["Recent"].(map[string]any)
	if value, present := unknown["modified_at"]; !present || value != nil || stated["FileModified"].(map[string]any)["Value"] != nil ||
		stated["Saved"].(map[string]any)["Raw"] != "2026-10-11T11:00:00" || stated["Saved"].(map[string]any)["Value"] != nil ||
		stated["LastAccessed"].(map[string]any)["Value"] != "2026-10-11T11:00:00Z" {
		t.Fatalf("an access, saved or capture clock stood in for an unknown modification time: %v", unknown)
	}
	if collaborator := usageCLIItem(t, rows, "collaborator", "person-1"); collaborator["modified_at"] != nil || collaborator["title"] != "" {
		t.Fatalf("a collaborator gained a modification time or a title: %v", collaborator)
	}
	// --since/--until read the native modification time only.
	for window, want := range map[[2]string]int{
		{"2026-10-09", "2026-10-09"}:                     2, // the document and its recent row
		{"2026-10-11T10:00:00Z", "2026-10-11T12:00:00Z"}: 0, // access time
		{"2026-10-08T08:00:00Z", "2026-10-08T10:00:00Z"}: 0, // sharing time
		{"2026-10-10T09:00:00Z", "2026-10-10T11:00:00Z"}: 0, // row-changed time
		{"2026-10-11T02:30:00Z", "2026-10-11T03:30:00Z"}: 0, // capture time
	} {
		doc, _ := usageCLIRun(t, e, "docs", "list", "--application", "onedrive", "--since", window[0], "--until", window[1])
		if got := len(items(t, doc)); got != want {
			t.Fatalf("window %v matched %d usage rows, want %d", window, got, want)
		}
	}
	// An Office JSON row keeps exactly the keys it had.
	doc, _ = usageCLIRun(t, e, "docs", "list", "--surface", "shared")
	legacy := items(t, doc)[0]
	for _, key := range []string{"variant", "usage_native", "full_inspection", "registry_native", "opened_at"} {
		if _, present := legacy[key]; present {
			t.Fatalf("Office JSON row gained %s", key)
		}
	}
	// Detail: the relations of the row's served capture, and the same facts in words.
	id := document["id"].(string)
	doc, _ = usageCLIRun(t, e, "docs", "show", id)
	detail := items(t, doc)[0]
	relations := detail["usage_relations"].(map[string]any)
	related := relations["items"].([]any)
	if relations["total"] != float64(1) || relations["truncated"] != false || len(related) != 1 || relations["capture_id"] != document["capture_id"] ||
		related[0].(map[string]any)["id"] != recent["id"] || related[0].(map[string]any)["type"] != "recent" || related[0].(map[string]any)["native_id"] != "native-clock" {
		t.Fatalf("relations: %v", relations)
	}
	doc, _ = usageCLIRun(t, e, "docs", "show", usageCLIItem(t, rows, "collaborator", "person-1")["id"].(string))
	if relations := items(t, doc)[0]["usage_relations"].(map[string]any); relations["total"] != float64(0) || len(relations["items"].([]any)) != 0 {
		t.Fatalf("collaborator relations: %v", relations)
	}
	text := usageCLIText(t, e, "--format", "text", "docs", "show", id)
	for _, want := range []string{id, "OneDrive usage document", "usage native claims", "yesterday afternoon", "modified_at: 2026-10-09T12:34:56.5Z",
		"sharing history: 1 captured, 1 shown", "related observations in capture", recent["id"].(string), "not_captured", "never opened",
		"full archived record: m365crawl --db", "unresolved"} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage text detail lacks %q:\n%s", want, text)
		}
	}
	text = usageCLIText(t, e, "--format", "text", "docs", "show", unknown["id"].(string))
	if !strings.Contains(text, "modified_at: unknown") || !strings.Contains(text, "OneDrive usage recent row") || strings.Contains(text, "surface ") {
		t.Fatalf("recent text detail:\n%s", text)
	}
	text = usageCLIText(t, e, "--format", "text", "docs", "show", usageCLIItem(t, rows, "collaborator", "person-1")["id"].(string))
	if !strings.Contains(text, "OneDrive usage collaborator") || !strings.Contains(text, "a collaborator profile states no modification time") ||
		!strings.Contains(text, "related observations in capture 0 (same native ID, not merged): 0 (returned 0; truncated=false)\n") {
		t.Fatalf("collaborator text detail:\n%s", text)
	}
	// A document whose modification text is not a clock says so, and counts its latest claim too.
	latest := usageCLIShare("Latest sharer", 2)
	unparsed := usageCLIDocument("native-words", "Unparsed", usageCLIShare("Sharer", 3))
	unparsed.ModifiedRaw, unparsed.Latest = "last week", &latest
	native, _ := usageNativeOf(store.UsageNative{Type: store.UsageTypeDocument, Document: &unparsed}, 0)
	lines := strings.Join(usageDetailLines(documentDetailItem{documentItem: documentItem{UsageNative: &native, FullInspection: "synthetic command"},
		UsageRelations: &usageRelationsItem{CaptureID: 7, Total: 2, Truncated: true, Items: []usageRelationItem{{ID: "document-observation:9", Type: "recent"}}}}), "\n")
	for _, want := range []string{"modified_at: unknown (the file gave none, or Document.Modified.Raw is not an RFC 3339 time with an offset)",
		"sharing history: 1 captured, 1 shown; participants: 3 captured, 3 shown", "latest sharing claim participants: 2 captured, 2 shown",
		"related observations in capture 7 (same native ID, not merged): 2 (returned 1; truncated=true); document-observation:9 recent",
		"full archived record: synthetic command"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("usage detail lines lack %q:\n%s", want, lines)
		}
	}
	if label := usageLabel(documentItem{UsageNative: &usageNativeItem{Document: &usageDocumentItem{ID: "native-untitled"}}}); label != "native-untitled (untitled)" {
		t.Fatalf("untitled document label = %q", label)
	}
	text = usageCLIText(t, e, "--format", "text", "docs", "list", "--application", "onedrive")
	for _, want := range []string{"Clock usage document", "usage_type", "recent", "collaborator", "onedrive"} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage text list lacks %q:\n%s", want, text)
		}
	}
	// Archive-only reads never discover, capture or write, on this schema or the one before it.
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	runDocumentsSync = func(ctx context.Context, options syncer.DocumentOptions) (syncer.DocumentReport, error) {
		if options.Discover || options.Explicit || options.UsageFile != "" || options.RegistryFile != "" || options.Root != "" {
			t.Errorf("an ordinary read discovered or forced capture: %+v", options)
		}
		return run(ctx, options)
	}
	reads := [][]string{{"docs", "list"}, {"docs", "search", "Clock"}, {"docs", "show", id}, {"docs", "sources"},
		{"search", "Clock", "--source", "documents"}, {"search", "Clock"}, {"--format", "text", "docs", "list"}, {"--format", "text", "docs", "show", id}}
	unchanged := func(name string, wantCode map[string]int) {
		t.Helper()
		before, err := os.ReadFile(e.db)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(e.db)
		if err != nil {
			t.Fatal(err)
		}
		for _, args := range reads {
			args = append([]string{"--max-age", "0"}, args...)
			code, first, stderr := e.run(args...)
			_, second, _ := e.run(args...)
			if code != wantCode[args[len(args)-2]+" "+args[len(args)-1]] || first != second {
				t.Fatalf("%s: %v is not a repeatable read: %d %s", name, args, code, stderr)
			}
		}
		after, err := os.ReadFile(e.db)
		if err != nil {
			t.Fatal(err)
		}
		current, err := os.Stat(e.db)
		if err != nil || !bytes.Equal(before, after) || !current.ModTime().Equal(info.ModTime()) {
			t.Fatalf("%s: an archive-only document read wrote the archive", name)
		}
	}
	unchanged("schema 15", nil)
	// The same archive at schema 14: usage rows cannot be read there, and nothing migrates it.
	e.exec(`delete from document_fts where rowid in (select rowid from document_observations where variant='onedrive_usage');
delete from document_capture_members where observation_rowid in (select rowid from document_observations where variant='onedrive_usage');
delete from document_observations where variant='onedrive_usage';
alter table document_observations drop column usage_json;
alter table document_observations drop column native_key;
drop table document_usage_relations;
update schema_migrations set version=14`)
	unchanged("schema 14", map[string]int{"show " + id: 2})
	doc, _ = usageCLIRun(t, e, "sql", "select max(version) as v from schema_migrations")
	if rows, _ := doc["rows"].([]any); len(rows) != 1 || rows[0].([]any)[0] != float64(14) {
		t.Fatal("an archive-only read migrated a schema 14 archive")
	}
	doc, _ = usageCLIRun(t, e, "docs", "list")
	if rows := items(t, doc); len(rows) != 1 || rows[0]["usage_native"] != nil {
		t.Fatalf("schema 14 archive rows: %v", rows)
	}
}

func TestUsageSyncFlagsDiscoveryAndRemedies(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dir, "usage copy.db")
	if err := os.WriteFile(abs, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	var got syncer.DocumentOptions
	called := false
	runDocumentsSync = func(_ context.Context, options syncer.DocumentOptions) (syncer.DocumentReport, error) {
		got, called = options, true
		return syncer.DocumentReport{Sources: []store.KnowledgeSource{{ID: 1, AdmittedID: 1, Kind: store.UsageDocumentKind, Status: "unchanged"}},
			Discovery: []syncer.DocumentDiscovery{
				{Kind: store.UsageDocumentKind, Code: "onedrive_usage_unavailable"},
				{Kind: store.UsageDocumentKind, Code: "onedrive_usage_discovery_unsupported", Locator: "/fictional/ListSync/Business_other"},
				{Kind: store.UsageDocumentKind, Code: "onedrive_usage_unavailable", Locator: "/fictional/ListSync/Business_one/Microsoft.FileUsageSync.db"},
				{Kind: store.UsageDocumentKind, Code: "onedrive_usage_default_unsupported"},
				{Kind: store.UsageDocumentKind, Code: "onedrive_usage_input_unqualified"},
				{Kind: store.UsageDocumentKind, Code: "onedrive_usage_future_code"}}}, nil
	}
	for _, args := range [][]string{
		{"docs", "sync", "--onedrive-usage-file", abs, "--root", dir},
		{"docs", "sync", "--onedrive-usage-file", abs, "--registry-file", abs},
		{"docs", "sync", "--onedrive-usage-file", abs, "--source-id", "1"},
		{"docs", "sync", "--onedrive-usage-file", "relative.db"},
		{"docs", "sync", "--onedrive-usage-file", abs + string(filepath.Separator) + ".." + string(filepath.Separator) + "other.db"},
		{"docs", "sync", "--onedrive-usage-file", filepath.Join(dir, "missing.db")},
		{"docs", "sync", "--onedrive-usage-file", dir},
		{"docs", "sync", "--onedrive-usage-file", e.db},
		{"docs", "sync", "--onedrive-usage-file", e.db + "-wal"},
	} {
		called = false
		if code, stdout, stderr := e.run(args...); code != 2 || stdout != "" || called || !strings.Contains(stderr, "--onedrive-usage-file") || !strings.Contains(stderr, `"usage"`) {
			t.Fatalf("invalid selection accepted: %v %d %s", args, code, stderr)
		}
	}
	code, stdout, stderr := e.run("docs", "sync", "--onedrive-usage-file", abs)
	if _, present := decode(t, stdout)["discovery"]; !present || code != 0 || got.UsageFile != abs || got.RegistryFile != "" || got.Root != "" || !got.Discover || !got.Explicit {
		t.Fatalf("usage file not routed: %d %+v %s", code, got, stderr)
	}
	code, stdout, stderr = e.run("docs", "sync")
	if code != 0 || got.UsageFile != "" || !got.Discover || !got.Explicit {
		t.Fatalf("default sync: %d %+v %s", code, got, stderr)
	}
	discovery, _ := decode(t, stdout)["discovery"].([]any)
	if len(discovery) != 6 {
		t.Fatalf("usage discovery outcomes missing: %s", stdout)
	}
	for i, raw := range discovery {
		outcome := raw.(map[string]any)
		meaning, _ := outcome["meaning"].(string)
		_, located := outcome["locator"]
		if outcome["kind"] != store.UsageDocumentKind || outcome["code"] == "" || outcome["source_id"] != nil || located != (i == 1 || i == 2) ||
			!strings.Contains(meaning, "m365crawl docs sync --onedrive-usage-file FILE") || !strings.Contains(meaning, "Nothing was registered or read") {
			t.Fatalf("discovery outcome %d is not a fixed sourceless outcome with a route: %v", i, outcome)
		}
	}
	code, stdout, _ = e.run("--format", "text", "docs", "sync")
	if code != 0 || !strings.Contains(stdout, "discovery onedrive_usage: onedrive_usage_unavailable") ||
		!strings.Contains(stdout, "discovery onedrive_usage ListSync/Business_other: onedrive_usage_discovery_unsupported") {
		t.Fatalf("text discovery outcomes: %s", stdout)
	}
	// The runner's own refusal (a file that changed after the command's check) is the same usage error.
	runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
		return syncer.DocumentReport{}, syncer.ErrUsageFileUnusable
	}
	if code, _, stderr := e.run("docs", "sync", "--onedrive-usage-file", abs); code != 2 || !strings.Contains(stderr, "--onedrive-usage-file") ||
		!strings.Contains(stderr, "nothing was registered") {
		t.Fatalf("runner refusal: %d %s", code, stderr)
	}
	runDocumentsSync = run
	if code, stdout, _ := e.run("--max-age", "0", "docs", "sources"); code != 0 || len(items(t, decode(t, stdout))) != 1 {
		t.Fatalf("a usage error registered a source: %s", stdout)
	}
	if code, _, stderr := e.run("--max-age", "0", "docs", "list", "--application", "dropbox"); code != 2 || !strings.Contains(stderr, "onedrive") {
		t.Fatalf("--application error does not name onedrive: %d %s", code, stderr)
	}
	// Nothing usable in any family stays a coded failure that names all three and every route.
	nothing := documentsNothingUsable(syncer.DocumentReport{Discovery: []syncer.DocumentDiscovery{
		{Kind: syncer.DocumentFamilyJSON, Code: "office_json_input_unqualified"}, {Kind: store.RegistryDocumentKind, Code: "office_registry_unavailable"},
		{Kind: store.UsageDocumentKind, Code: "onedrive_usage_discovery_unsupported", Locator: "/fictional/ListSync/Business_other"}}})
	if nothing == nil || !strings.Contains(nothing.Error(), "onedrive_usage: onedrive_usage_discovery_unsupported") {
		t.Fatalf("nothing usable: %v", nothing)
	}
	for _, usable := range [][]syncer.DocumentDiscovery{
		{{Kind: syncer.DocumentFamilyJSON, Code: "office_json_input_unqualified"}, {Kind: store.RegistryDocumentKind, Code: "office_registry_unavailable"}},
		{{Kind: syncer.DocumentFamilyJSON, Code: "office_json_input_unqualified"}, {Kind: store.UsageDocumentKind, Code: "onedrive_usage_unavailable"}},
	} {
		if err := documentsNothingUsable(syncer.DocumentReport{Discovery: usable}); err != nil {
			t.Fatalf("a family with no outcome was usable, so the sync reports: %v", err)
		}
	}
}

func TestUsageCoverageNoticesAndRetryQuoting(t *testing.T) {
	coverage := (&runtime{}).documentsCoverage([]store.KnowledgeSource{
		{ID: 1, AdmittedID: 1, Kind: store.UsageDocumentKind, Status: "partial", Losses: map[string]int{"onedrive_usage_document_unmapped": 2,
			"onedrive_usage_recent_unmapped": 1, "onedrive_usage_document_orphan": 3, "onedrive_usage_collaborator_unmapped": 4}},
		{ID: 2, AdmittedID: 2, Kind: store.UsageDocumentKind, Status: "complete", Reason: syncer.UsageListedNothing},
		{Kind: store.UsageDocumentKind, Platform: "windows", Status: "failed", Reason: "onedrive_usage_future_reason", Locator: `C:\synthetic\copy one.db`},
		{Kind: store.UsageDocumentKind, Platform: "darwin", Status: "cancelled", Reason: "onedrive_usage_cancelled", Locator: "/synthetic/it's.db"},
		{ID: 5, AdmittedID: 5, Kind: store.UsageDocumentKind, Platform: "darwin", Status: "failed", Reason: "onedrive_usage_source_busy", Locator: "/synthetic/busy.db"},
		{Kind: store.UsageDocumentKind, Platform: "windows", Status: "failed", Reason: "onedrive_usage_input_unqualified", Locator: `C:\synthetic\we$(calc).db`},
	})
	prefixes := []string{syncer.UsageListedNothing, "onedrive_usage_recent_unmapped", "onedrive_usage_document_unmapped",
		"onedrive_usage_document_orphan", "onedrive_usage_collaborator_unmapped"}
	if len(coverage.Notices) != len(prefixes) || coverage.Sources[0].Fix != "" || coverage.Sources[1].Fix != "" {
		t.Fatalf("usage notices: %+v", coverage.Notices)
	}
	for i, prefix := range prefixes {
		if !strings.HasPrefix(coverage.Notices[i], prefix+":") {
			t.Fatalf("notice %d = %q, want %s", i, coverage.Notices[i], prefix)
		}
	}
	if fix := coverage.Sources[2].Fix; !strings.Contains(fix, `docs sync --onedrive-usage-file "C:\synthetic\copy one.db"`) || !strings.Contains(fix, "private copy") {
		t.Fatalf("unknown usage reason or Windows path retry: %s", fix)
	}
	if fix := coverage.Sources[3].Fix; !strings.Contains(fix, `docs sync --onedrive-usage-file '/synthetic/it'\''s.db'`) {
		t.Fatalf("quoted retry: %s", fix)
	}
	if source := coverage.Sources[4]; !strings.Contains(source.Fix, "docs sync --source-id 5") || strings.Contains(source.Fix, "--onedrive-usage-file") ||
		!strings.Contains(source.Fix, "OneDrive") || !strings.Contains(source.Effect, "last good capture") {
		t.Fatalf("admitted usage failure route: %+v", source)
	}
	if fix := coverage.Sources[5].Fix; strings.Contains(fix, "$(calc)") || !strings.Contains(fix, "docs sync --onedrive-usage-file FILE") || !strings.Contains(fix, "locator") {
		t.Fatalf("windows retry interpolated an unsafe name: %s", fix)
	}
	for _, reason := range documentFailureReasons {
		if documentFailureFix[reason] == "" {
			t.Fatalf("failure reason %s has no remedy", reason)
		}
	}
	for _, reason := range []string{"onedrive_usage_input_unqualified", "onedrive_usage_source_busy", "onedrive_usage_index_unreadable",
		"onedrive_usage_index_too_large", "onedrive_usage_index_unrecognized", "onedrive_usage_capture_failed", "onedrive_usage_cleanup_failed",
		"onedrive_usage_policy_restore_failed", "onedrive_usage_cancelled"} {
		found := false
		for _, known := range documentFailureReasons {
			found = found || known == reason
		}
		if !found {
			t.Fatalf("usage failure reason %s is not documented", reason)
		}
	}
}

const usageCLIFixtureSQL = `
CREATE TABLE recent_files_spo(ID TEXT PRIMARY KEY,ItemId TEXT,ListId TEXT,WebId TEXT,SiteId TEXT,
LastModifiedDateTime,LastAccessedDateTime,FileModifiedDateTime,LastSharedDateTime,SavedDateTime,
Type TEXT,Source TEXT,Extension TEXT,WorkingSetId TEXT,HiddenFromShared INTEGER,IsLocalPlaceHolder INTEGER);
CREATE TABLE recent_files_formatted_spo(ID TEXT,Format TEXT NOT NULL,FormattedValue TEXT NOT NULL,PRIMARY KEY(ID,Format));
CREATE TABLE top_collaborators(ID TEXT PRIMARY KEY,FormattedValue TEXT);
INSERT INTO recent_files_spo(ID,FileModifiedDateTime,Extension) VALUES('synthetic-id','2026-10-09T12:34:56Z','docx'),('recent-only',NULL,'xlsx');
INSERT INTO top_collaborators VALUES('native-person','{"Id":"stated-person","DisplayName":"Synthetic Person","EmailAddresses":["person@example.invalid"]}');
`

// A supplied ordinary copy runs the whole path: capture, list, unchanged retry. Where the
// platform qualifies no input, the same command records the fixed refusal and its remedy.
func TestUsageCLISuppliedFileEndToEnd(t *testing.T) {
	e := newEnv(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "Microsoft.FileUsageSync.db")
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(file), "/")}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(`{"file":{"FileName":"Synthetic usage title","FileModifiedTime":"2026-10-09T12:34:56Z"}}`)
	for _, statement := range []string{usageCLIFixtureSQL, "INSERT INTO recent_files_formatted_spo VALUES('synthetic-id','variant-a','" +
		strings.Trim(string(body), `"`) + "')"} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file) //nolint:gosec // G304: the test's own temporary file
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := e.run("docs", "sync", "--onedrive-usage-file", file)
	if code != 0 {
		t.Fatalf("a supplied usage file is a source outcome, not a command error: %d %s", code, stderr)
	}
	doc := decode(t, stdout)
	sources := coverageSources(t, doc)
	if len(sources) != 1 || sources[0]["kind"] != store.UsageDocumentKind {
		t.Fatalf("usage source: %s", stdout)
	}
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		fix, _ := sources[0]["fix"].(string)
		if doc["failed"] != float64(1) || sources[0]["source_id"] != nil || sources[0]["reason"] != "onedrive_usage_input_unqualified" ||
			!strings.Contains(fix, "docs sync --onedrive-usage-file "+quoteFor("")(file)) {
			t.Fatalf("unqualified platform refusal: %s", stdout)
		}
		return
	}
	if doc["captured"] != float64(1) || sources[0]["status"] != "complete" || sources[0]["qualification"] != store.UsageDocumentQualification {
		t.Fatalf("usage capture: %s", stdout)
	}
	listed, _ := usageCLIRun(t, e, "docs", "list", "--application", "onedrive")
	rows := items(t, listed)
	if len(rows) != 4 || usageCLIItem(t, rows, "document", "synthetic-id")["title"] != "Synthetic usage title" ||
		usageCLIItem(t, rows, "recent", "recent-only")["modified_at"] != nil {
		t.Fatalf("captured usage rows: %v", rows)
	}
	code, stdout, _ = e.run("docs", "sync", "--onedrive-usage-file", file)
	if doc := decode(t, stdout); code != 0 || doc["unchanged"] != float64(1) || doc["captured"] != float64(0) {
		t.Fatalf("unchanged retry: %s", stdout)
	}
	after, err := os.ReadFile(file) //nolint:gosec // G304: the test's own temporary file
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("the supplied usage file changed")
	}
	for _, leftover := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(file + leftover); err == nil {
			t.Fatalf("capture left %s beside the source", leftover)
		}
	}
}

// Usage rows count toward the documents source of a shared search like any other document row:
// exact when the result holds them all, a lower bound when the reading stopped at the limit, and
// no hint points at the source already selected.
func TestUsageSearchSourceCounts(t *testing.T) {
	e := newEnv(t)
	e.sync()
	result := onedriveusage.Result{}
	for _, id := range []string{"native-q1", "native-q2", "native-q3"} {
		result.Recent = append(result.Recent, usageCLIRecent(id))
		result.Documents = append(result.Documents, usageCLIDocument(id, "Synthetic quartzword plan "+id))
	}
	seedUsageCLI(t, e, "/synthetic/usage/Microsoft.FileUsageSync.db", result)
	documents := func(doc map[string]any) map[string]any {
		t.Helper()
		sources, _ := doc["sources"].(map[string]any)
		out, _ := sources["documents"].(map[string]any)
		if out == nil {
			t.Fatalf("search reports no documents source: %v", doc["sources"])
		}
		return out
	}
	all, _ := usageCLIRun(t, e, "--max-age", "0", "search", "quartzword")
	if got := documents(all); got["returned"] != float64(3) || got["matched"] != float64(3) || got["truncated"] != false {
		t.Fatalf("documents source of a complete search = %v", got)
	}
	for _, item := range all["items"].([]any) {
		if row := item.(map[string]any); row["source"] != "documents" || row["variant"] != "onedrive_usage" {
			t.Fatalf("search item is not a usage document row: %v", row)
		}
	}
	cut, _ := usageCLIRun(t, e, "--max-age", "0", "search", "quartzword", "--limit", "2")
	got := documents(cut)
	if _, exact := got["matched"]; exact || got["returned"] != float64(2) || got["matched_at_least"] == nil || got["truncated"] != true {
		t.Fatalf("documents source of a cut search = %v", got)
	}
	for _, args := range [][]string{{"search", "quartzword", "--limit", "2"}, {"search", "quartzword", "--limit", "2", "--source", "documents"}} {
		text := usageCLIText(t, e, append([]string{"--max-age", "0", "--format", "text"}, args...)...)
		if strings.Contains(text, "use --source documents") && slices.Contains(args, "--source") {
			t.Fatalf("%v: the hint names the source already selected:\n%s", args, text)
		}
		// The reading stopped at the limit, so no count of hidden hits is known or printed.
		if strings.Contains(text, "not shown") || !strings.Contains(text, "more exist; raise --limit") {
			t.Fatalf("%v: a cut search must say to raise --limit without inventing a hidden count:\n%s", args, text)
		}
	}
}

// printedStrings is what a command printed, as the reader sees it: every string value of a JSON
// document, or the text itself.
func printedStrings(stdout string) []string {
	var doc any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		return []string{stdout}
	}
	var out []string
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case string:
			out = append(out, typed)
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(doc)
	return out
}

// The follow-up scan reads JSON output as decoded strings, so an archive path with backslashes
// (every Windows path) is compared as the reader sees it and not as JSON escapes it.
func TestPrintedStringsDecodeJSONPaths(t *testing.T) {
	command := `m365crawl --db "C:\\synthetic dir\\m365crawl.db" --max-age 0 sql`
	encoded, err := json.Marshal(map[string]any{"items": []any{map[string]any{"full_inspection": command}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), command) {
		t.Fatal("the fixture path is not escaped by JSON, so it proves nothing")
	}
	if got := printedStrings(string(encoded)); len(got) != 1 || got[0] != command {
		t.Fatalf("decoded strings = %q", got)
	}
	if got := printedStrings("plain text\n" + command); len(got) != 1 || !strings.Contains(got[0], command) {
		t.Fatalf("text output = %q", got)
	}
}
