package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/officeregistry"
	"github.com/ourostack/m365crawl/internal/onedriveusage"
)

const usageLocator = "/fixture/Business_synthetic/Microsoft.FileUsageSync.db"

func usageShare(name string, participants ...string) onedriveusage.Share {
	share := onedriveusage.Share{DisplayName: name, SMTP: strings.ToLower(name) + "@example.invalid", AadID: "aad-" + name,
		AtRaw: "2026-10-08T09:00:00Z", ConversationID: "19:synthetic-thread@thread.v2", Subject: "Synthetic subject " + name}
	for _, participant := range participants {
		share.Participants = append(share.Participants, onedriveusage.Person{DisplayName: participant, SMTP: strings.ToLower(participant) + "@example.invalid"})
	}
	return share
}

func usageDocument(id, format, title string, history ...onedriveusage.Share) onedriveusage.DocumentObservation {
	return onedriveusage.DocumentObservation{ID: id, Format: format, Title: title, URL: "https://example.invalid/sites/synthetic/" + id + ".docx",
		Extension: "docx", Owner: "Synthetic Owner", CreatedRaw: "2026-10-01T08:00:00Z", ModifiedRaw: "2026-10-09T14:34:56.5+02:00",
		SiteID: "site-" + id, WebID: "web-" + id, ListID: "list-" + id, UniqueID: "unique-" + id, History: history}
}

func usageRecent(id string) onedriveusage.RecentObservation {
	return onedriveusage.RecentObservation{ID: id, ItemID: "item-" + id, ListID: "list-" + id, WebID: "web-" + id, SiteID: "site-" + id,
		Type: "Word", Source: "native-source", Extension: "docx", WorkingSetID: "working-" + id,
		LastModifiedRaw: "2026-10-10T10:00:00Z", LastAccessedRaw: "2026-10-11T11:00:00Z", FileModifiedRaw: "2026-10-09T12:34:56Z",
		LastSharedRaw: "2026-10-08T09:00:00Z"}
}

func usageCollaborator(id string) onedriveusage.Collaborator {
	return onedriveusage.Collaborator{ID: id, UserID: "stated-" + id, DisplayName: "Synthetic Collaborator " + id,
		Department: "Synthetic Department", JobTitle: "Synthetic Title", Office: "Synthetic Office",
		Emails: []string{id + "@example.invalid"}}
}

func beginUsage(t *testing.T, s *Store, locator string, at time.Time) KnowledgeAttempt {
	t.Helper()
	attempt, err := s.BeginUsageDocumentAttempt(context.Background(), KnowledgeInput{
		Kind: UsageDocumentKind, Platform: "synthetic", Locator: locator, At: at})
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func admitUsage(t *testing.T, s *Store, attempt KnowledgeAttempt, at time.Time, status, digest string, result onedriveusage.Result) {
	t.Helper()
	if err := s.CommitUsageDocumentObservations(context.Background(), attempt, KnowledgeOutcome{
		Status: status, ContentDigest: digest, Qualification: UsageDocumentQualification, At: at.Add(time.Second),
		SourceObservedAt: at}, result, UsageCaptureMetadata{AcquiredAt: &at, SourceObservedAt: &at}); err != nil {
		t.Fatal(err)
	}
}

// usageRow finds the one row of a source with this native type, ID and format.
func usageRow(t *testing.T, rows []DocumentObservationRow, sourceID int64, kind, id, format string) DocumentObservationRow {
	t.Helper()
	var found []DocumentObservationRow
	for _, row := range rows {
		if row.Usage == nil || row.SourceID != sourceID || row.Usage.Type != kind {
			continue
		}
		switch {
		case kind == UsageTypeDocument && row.Usage.Document.ID == id && row.Usage.Document.Format == format,
			kind == UsageTypeRecent && row.Usage.Recent.ID == id, kind == UsageTypeCollaborator && row.Usage.Collaborator.ID == id:
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d rows for %s %s/%s in source %d", len(found), kind, id, format, sourceID)
	}
	return found[0]
}

func usageRows(t *testing.T, s *Store, filter DocumentFilter) []DocumentObservationRow {
	t.Helper()
	rows, _, err := s.DocumentObservations(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func usageRelations(t *testing.T, s *Store, id int64) DocumentObservationDetail {
	t.Helper()
	detail, err := s.DocumentObservation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

func relationIDs(detail DocumentObservationDetail) []int64 {
	out := []int64{}
	for _, relation := range detail.UsageRelations {
		out = append(out, relation.ObservationID)
	}
	return out
}

// Review focus 1: one native ID with two Format variants and a recent-only peer. Neither variant
// and no recent-only evidence disappears, and no variant is preferred.
func TestUsageVariantAndRecentOnly(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	result := onedriveusage.Result{
		Recent: []onedriveusage.RecentObservation{usageRecent("native-1"), usageRecent("native-2")},
		Documents: []onedriveusage.DocumentObservation{usageDocument("native-1", "variant-a", "Synthetic variant A"),
			usageDocument("native-1", "variant-b", "Synthetic variant B")},
		Collaborators: []onedriveusage.Collaborator{usageCollaborator("person-1")}}
	one := beginUsage(t, s, usageLocator, base)
	admitUsage(t, s, one, base, "complete", "a-one", result)
	// Another source states the same native IDs: the rows stay independent per source.
	other := beginUsage(t, s, "/fixture/Business_other/Microsoft.FileUsageSync.db", base)
	admitUsage(t, s, other, base, "complete", "b-one", result)
	rows := usageRows(t, s, DocumentFilter{})
	if len(rows) != 10 {
		t.Fatalf("%d rows, want 5 for each of 2 sources", len(rows))
	}
	sources, err := s.DocumentSources(ctx)
	if err != nil || len(sources) != 2 || sources[0].Kind != UsageDocumentKind || sources[0].Qualification != UsageDocumentQualification ||
		sources[0].Boundary != "unresolved" || sources[0].ID == sources[1].ID {
		t.Fatalf("usage sources: %+v %v", sources, err)
	}
	a, b := sources[0].ID, sources[1].ID
	for _, row := range rows {
		if row.Variant != UsageDocumentKind || row.Usage == nil || row.Application != UsageDocumentSource || row.Registry != nil ||
			row.OpenedAt != nil || !reflect.DeepEqual(row.Document, officedocuments.Document{}) || row.Retained {
			t.Fatalf("usage row is not a concrete native variant: %+v", row)
		}
		payloads := 0
		for _, present := range []bool{row.Usage.Document != nil, row.Usage.Recent != nil, row.Usage.Collaborator != nil} {
			if present {
				payloads++
			}
		}
		if payloads != 1 {
			t.Fatalf("row %d carries %d native payloads", row.ID, payloads)
		}
	}
	recentOne, recentOnly := usageRow(t, rows, a, UsageTypeRecent, "native-1", ""), usageRow(t, rows, a, UsageTypeRecent, "native-2", "")
	variantA, variantB := usageRow(t, rows, a, UsageTypeDocument, "native-1", "variant-a"), usageRow(t, rows, a, UsageTypeDocument, "native-1", "variant-b")
	collaborator := usageRow(t, rows, a, UsageTypeCollaborator, "person-1", "")
	if !reflect.DeepEqual(*recentOnly.Usage.Recent, usageRecent("native-2")) || !reflect.DeepEqual(*variantB.Usage.Document, result.Documents[1]) {
		t.Fatal("recent-only or second-variant payload changed")
	}
	// The recent row relates to every variant of its capture; the recent-only row to none.
	if detail := usageRelations(t, s, recentOne.ID); detail.UsageRelationsTotal != 2 || detail.UsageRelationsTruncated ||
		!reflect.DeepEqual(relationIDs(detail), []int64{variantA.ID, variantB.ID}) || detail.UsageRelationsCaptureID != one.ID ||
		detail.UsageRelations[0] != (DocumentUsageRelation{ObservationID: variantA.ID, Type: UsageTypeDocument, ID: "native-1", Format: "variant-a"}) {
		t.Fatalf("recent row relations = %+v", detail.UsageRelations)
	}
	if detail := usageRelations(t, s, recentOnly.ID); detail.UsageRelationsTotal != 0 || len(detail.UsageRelations) != 0 || detail.UsageRelationsCaptureID != one.ID {
		t.Fatalf("recent-only row gained relations: %+v", detail.UsageRelations)
	}
	if detail := usageRelations(t, s, variantB.ID); detail.UsageRelationsTotal != 1 ||
		detail.UsageRelations[0] != (DocumentUsageRelation{ObservationID: recentOne.ID, Type: UsageTypeRecent, ID: "native-1"}) {
		t.Fatalf("variant relations = %+v", detail.UsageRelations)
	}
	if detail := usageRelations(t, s, collaborator.ID); detail.UsageRelationsTotal != 0 || detail.UsageRelationsCaptureID != 0 || detail.Source.ID != a {
		t.Fatalf("collaborator row gained relations: %+v", detail)
	}
	// The other source's equal native IDs are never joined to this one's.
	otherRecent := usageRow(t, rows, b, UsageTypeRecent, "native-1", "")
	for _, id := range relationIDs(usageRelations(t, s, otherRecent.ID)) {
		if id == variantA.ID || id == variantB.ID {
			t.Fatal("a relation crossed sources through an equal native ID")
		}
	}
	// A collaborator is a source claim: it creates no person, alias or people observation.
	for _, table := range []string{"people", "people_observations"} {
		if n := rowCount(t, s, "select count(*) from "+table); n != 0 {
			t.Fatalf("usage capture wrote %d rows to %s", n, table)
		}
	}
	// An unchanged recent row and a changed, a removed and a new variant: each capture keeps its
	// own exact edges, and the earlier capture's edges are not rewritten.
	changed := usageDocument("native-1", "variant-a", "Synthetic variant A, renamed")
	two := beginUsage(t, s, usageLocator, base.Add(time.Hour))
	admitUsage(t, s, two, base.Add(time.Hour), "complete", "a-two", onedriveusage.Result{
		Recent:    []onedriveusage.RecentObservation{usageRecent("native-1")},
		Documents: []onedriveusage.DocumentObservation{changed, usageDocument("native-1", "variant-c", "Synthetic variant C")}})
	rows = usageRows(t, s, DocumentFilter{SourceID: a})
	if len(rows) != 7 {
		t.Fatalf("%d rows after the second capture, want 7", len(rows))
	}
	reused := usageRow(t, rows, a, UsageTypeRecent, "native-1", "")
	if reused.ID != recentOne.ID || reused.CaptureID != one.ID || reused.Retained || !reused.CapturedAt.Equal(base) {
		t.Fatalf("unchanged recent row was not reused with its original clocks: %+v", reused)
	}
	var renamed, variantC DocumentObservationRow
	for _, row := range rows {
		if row.Usage.Type != UsageTypeDocument {
			continue
		}
		switch {
		case row.Usage.Document.Title == changed.Title:
			renamed = row
		case row.Usage.Document.Format == "variant-c":
			variantC = row
		}
	}
	edges := func(capture int64) string {
		var out sql.NullString
		if err := s.db.QueryRow(`select group_concat(recent_rowid||'>'||document_rowid,',') from
(select * from document_usage_relations where capture_id=? order by recent_rowid,document_rowid)`, capture).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out.String
	}
	wantOne := strings.Join([]string{edge(recentOne.ID, variantA.ID), edge(recentOne.ID, variantB.ID)}, ",")
	wantTwo := strings.Join([]string{edge(recentOne.ID, renamed.ID), edge(recentOne.ID, variantC.ID)}, ",")
	if edges(one.ID) != wantOne || edges(two.ID) != wantTwo {
		t.Fatalf("capture edges: first %q (want %q), second %q (want %q)", edges(one.ID), wantOne, edges(two.ID), wantTwo)
	}
	if detail := usageRelations(t, s, recentOne.ID); detail.UsageRelationsCaptureID != two.ID ||
		!reflect.DeepEqual(relationIDs(detail), []int64{renamed.ID, variantC.ID}) {
		t.Fatalf("current recent row is not bound to its served capture: %+v", detail.UsageRelations)
	}
	// A row absent from the current capture is served from the last capture that held it.
	if detail := usageRelations(t, s, variantB.ID); !detail.Observation.Retained || detail.UsageRelationsCaptureID != one.ID ||
		!reflect.DeepEqual(relationIDs(detail), []int64{recentOne.ID}) {
		t.Fatalf("retained variant lost its own capture's relation: %+v", detail)
	}
}

func edge(recent, document int64) string {
	return strconv.FormatInt(recent, 10) + ">" + strconv.FormatInt(document, 10)
}

// Review focus 3: when sharing history rolls off, or a later capture fails, the original claims
// and clocks stay attributed to the capture that stated them and are never reclassified current.
func TestUsageRetainedHistoryAndFrontiers(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	at := func(hours int) time.Time { return base.Add(time.Duration(hours) * time.Hour) }
	first := beginUsage(t, s, usageLocator, base)
	if err := s.RecordKnowledgeFailure(ctx, first, KnowledgeOutcome{Status: "failed", Reason: "onedrive_usage_source_busy", At: base}); err != nil {
		t.Fatal(err)
	}
	sources, err := s.DocumentSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].ID != 0 || sources[0].AdmittedID != 0 || sources[0].Reason != "onedrive_usage_source_busy" ||
		sources[0].Kind != UsageDocumentKind {
		t.Fatalf("first failure invented an admitted source: %+v %v", sources, err)
	}
	full := usageDocument("native-1", "variant-a", "Synthetic shared document", usageShare("Alpha", "One"), usageShare("Beta"), usageShare("Gamma"))
	rolled := usageDocument("native-1", "variant-a", "Synthetic shared document", usageShare("Beta"), usageShare("Gamma"))
	capture := func(document onedriveusage.DocumentObservation) onedriveusage.Result {
		return onedriveusage.Result{Recent: []onedriveusage.RecentObservation{usageRecent("native-1")}, Documents: []onedriveusage.DocumentObservation{document}}
	}
	one := beginUsage(t, s, usageLocator, at(1))
	admitUsage(t, s, one, at(1), "complete", "digest-full", capture(full))
	two := beginUsage(t, s, usageLocator, at(2))
	admitUsage(t, s, two, at(2), "complete", "digest-rolled", capture(rolled))
	documents := func() (withHistory, withoutFirst DocumentObservationRow) {
		for _, row := range usageRows(t, s, DocumentFilter{}) {
			if row.Usage.Type != UsageTypeDocument {
				continue
			}
			if len(row.Usage.Document.History) == 3 {
				withHistory = row
			} else {
				withoutFirst = row
			}
		}
		return withHistory, withoutFirst
	}
	original, current := documents()
	if !original.Retained || original.CaptureID != one.ID || !original.CapturedAt.Equal(at(1)) || !original.SourceObservedAt.Equal(at(1)) ||
		!reflect.DeepEqual(*original.Usage.Document, full) {
		t.Fatalf("rolled-off history lost its original claims, clocks or retained state: %+v", original)
	}
	if current.Retained || current.CaptureID != two.ID || len(current.Usage.Document.History) != 2 {
		t.Fatalf("current representation: %+v", current)
	}
	// A-B-A: the first state returns and reuses its representation with its original clocks.
	three := beginUsage(t, s, usageLocator, at(3))
	admitUsage(t, s, three, at(3), "partial", "digest-full-2", onedriveusage.Result{Recent: capture(full).Recent, Documents: capture(full).Documents,
		Losses: []onedriveusage.Loss{{Code: "onedrive_usage_document_unmapped", Count: 2}}})
	original, current = documents()
	if len(usageRows(t, s, DocumentFilter{})) != 3 || original.Retained || original.CaptureID != one.ID || !original.CapturedAt.Equal(at(1)) || !current.Retained {
		t.Fatalf("A-B-A did not reuse the first representation: %+v / %+v", original, current)
	}
	four := beginUsage(t, s, usageLocator, at(4))
	admitUsage(t, s, four, at(4), "unchanged", "digest-full-2", onedriveusage.Result{})
	sources, err = s.DocumentSources(ctx)
	if err != nil || sources[0].AdmittedID != four.ID || sources[0].Coverage != "partial" || sources[0].Status != "unchanged" ||
		sources[0].Losses["onedrive_usage_document_unmapped"] != 2 {
		t.Fatalf("unchanged capture lost partial losses: %+v %v", sources, err)
	}
	original, _ = documents()
	// The unchanged capture shares the membership and relations of the capture that owns them.
	if detail := usageRelations(t, s, original.ID); original.Retained || detail.UsageRelationsCaptureID != three.ID || detail.UsageRelationsTotal != 1 {
		t.Fatalf("unchanged capture lost membership or relations: %+v", detail)
	}
	// Failed after good: the last good frontier, its rows and their clocks stay as they were.
	five := beginUsage(t, s, usageLocator, at(5))
	if err := s.RecordKnowledgeFailure(ctx, five, KnowledgeOutcome{Status: "failed", Reason: "onedrive_usage_capture_failed", At: at(5)}); err != nil {
		t.Fatal(err)
	}
	sources, err = s.DocumentSources(ctx)
	original, current = documents()
	if err != nil || sources[0].Status != "failed" || sources[0].AdmittedID != four.ID || sources[0].Reason != "onedrive_usage_capture_failed" ||
		sources[0].Coverage != "partial" || original.Retained || original.CaptureID != one.ID || !current.Retained || current.CaptureID != two.ID {
		t.Fatalf("failed-after-good changed the frontier or reclassified rows: %+v %v", sources, err)
	}
	// Reverse attempt order: an older attempt finishing after a newer one is never admitted.
	old := beginUsage(t, s, usageLocator, at(6))
	newer := beginUsage(t, s, usageLocator, at(7))
	admitUsage(t, s, newer, at(7), "complete", "digest-rolled-2", capture(rolled))
	err = s.CommitUsageDocumentObservations(ctx, old, KnowledgeOutcome{Status: "complete", ContentDigest: "digest-old",
		Qualification: UsageDocumentQualification, At: at(8)}, capture(usageDocument("native-9", "variant-a", "Stale")), UsageCaptureMetadata{})
	if !errors.Is(err, ErrStaleKnowledgeAttempt) || len(usageRows(t, s, DocumentFilter{Query: "Stale"})) != 0 {
		t.Fatalf("older attempt admitted past the frontier: %v", err)
	}
	if err := s.CommitUsageDocumentObservations(ctx, beginUsage(t, s, usageLocator, at(9)), KnowledgeOutcome{Status: "unchanged",
		ContentDigest: "never-admitted", Qualification: UsageDocumentQualification, At: at(9)}, onedriveusage.Result{}, UsageCaptureMetadata{}); err == nil {
		t.Fatal("unchanged outcome admitted for a digest that was never admitted")
	}
	// A complete capture of a readable, empty source supersedes current membership and keeps history.
	empty := beginUsage(t, s, usageLocator, at(10))
	admitUsage(t, s, empty, at(10), "complete", "digest-empty", onedriveusage.Result{})
	rows := usageRows(t, s, DocumentFilter{})
	sources, err = s.DocumentSources(ctx)
	if err != nil || len(rows) != 3 || sources[0].AdmittedID != empty.ID || sources[0].Coverage != "observations" || sources[0].Status != "complete" {
		t.Fatalf("readable-empty capture: %d rows, %+v %v", len(rows), sources, err)
	}
	for _, row := range rows {
		if !row.Retained || row.CaptureID == empty.ID {
			t.Fatalf("history was erased or reclassified by an empty capture: %+v", row)
		}
	}
	original, _ = documents()
	if detail := usageRelations(t, s, original.ID); detail.UsageRelationsCaptureID != three.ID || detail.UsageRelationsTotal != 1 {
		t.Fatalf("retained row is not served from the last capture whose membership held it: %+v", detail)
	}
}

func TestUsageCompleteNativePayload(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	jsonAttempt := beginDocument(t, s, base)
	shared := documentFixture("Shared JSON document", 0)
	shared.URL = "https://example.invalid/sites/synthetic/native-1.docx"
	admitDocument(t, s, jsonAttempt, base, "complete", "json-one", []officedocuments.Document{shared})
	registry := beginRegistry(t, s, registryLocator, base)
	admitRegistry(t, s, registry, base, "complete", "registry-one", officeregistry.Result{Documents: []officeregistry.Observation{registryObservation(12, "Registry document")}})
	legacy := dumpTable(t, s, "document_observations") + "\n" + dumpTable(t, s, "document_fts") + "\n" + dumpTable(t, s, "document_capture_members")

	zero, count, size, recurring, hidden := int64(0), int64(7), int64(4096), false, int64(1)
	repeated := usageShare("Alpha", "One", "One", "")
	repeated.ParticipantsCount, repeated.Recurring = &count, &recurring
	repeated.MeetingStartRaw, repeated.ICalUID, repeated.MeetingSubject, repeated.AttachmentID = "not a clock", "ical-synthetic", "Synthetic meeting", "attachment-synthetic"
	bare := onedriveusage.Share{ParticipantsCount: &zero}
	full := usageDocument("native-1", "variant-a", "Synthetic complete document", repeated, repeated, bare, onedriveusage.Share{})
	full.Size = &size
	full.Latest = &onedriveusage.Share{DisplayName: "Latest Sharer", SMTP: "latest@example.invalid", AtRaw: "", ConversationID: "19:latest-thread@thread.v2"}
	titleless := onedriveusage.DocumentObservation{ID: "native-1", Format: "variant-b", ModifiedRaw: "10/09/2026 14:34"}
	recent := usageRecent("native-1")
	recent.HiddenFromShared = &hidden
	unknown := usageRecent("native-0")
	unknown.FileModifiedRaw = ""
	people := []onedriveusage.Collaborator{usageCollaborator("person-1"), {ID: "person-2"}, {ID: "person-3", Emails: []string{}}}
	result := onedriveusage.Result{
		// Deliberately not in native-key order: the store sorts ties by the full native key.
		Recent: []onedriveusage.RecentObservation{recent, unknown}, Documents: []onedriveusage.DocumentObservation{full, titleless},
		Collaborators: people,
		Losses: []onedriveusage.Loss{{Code: "onedrive_usage_collaborator_unmapped", Count: 1}, {Code: "onedrive_usage_document_orphan", Count: 2},
			{Code: "onedrive_usage_document_unmapped", Count: 3}, {Code: "onedrive_usage_recent_unmapped", Count: 4}}}
	attempt := beginUsage(t, s, usageLocator, base)
	admitUsage(t, s, attempt, base, "partial", "usage-one", result)
	if after := dumpTable(t, s, "document_observations") + "\n" + dumpTable(t, s, "document_fts") + "\n" + dumpTable(t, s, "document_capture_members"); !strings.HasPrefix(after, strings.Split(legacy, "\n")[0]) {
		t.Fatal("legacy rows moved")
	} else {
		for _, line := range strings.Split(legacy, "\n") {
			if !strings.Contains(after, line) {
				t.Fatalf("a usage capture changed a legacy row, index entry or membership: %s", line)
			}
		}
	}
	rows := usageRows(t, s, DocumentFilter{Application: UsageDocumentSource})
	if len(rows) != 7 {
		t.Fatalf("%d usage rows, want 7", len(rows))
	}
	sourceID := rows[0].SourceID
	document := usageRow(t, rows, sourceID, UsageTypeDocument, "native-1", "variant-a")
	if !reflect.DeepEqual(*document.Usage.Document, full) {
		t.Fatalf("complete native payload changed:\n got %+v\nwant %+v", *document.Usage.Document, full)
	}
	history := document.Usage.Document.History
	if len(history) != 4 || !reflect.DeepEqual(history[0], history[1]) || len(history[0].Participants) != 3 ||
		history[0].Participants[0] != history[0].Participants[1] || history[0].Participants[2] != (onedriveusage.Person{SMTP: "@example.invalid"}) ||
		*history[0].ParticipantsCount != 7 || *history[0].Recurring || history[2].ParticipantsCount == nil || *history[2].ParticipantsCount != 0 ||
		history[3].ParticipantsCount != nil || history[3].Recurring != nil || history[3].Participants != nil {
		t.Fatalf("duplicate positions, repeated participants or nullable counts changed: %+v", history)
	}
	bareRow := usageRow(t, rows, sourceID, UsageTypeDocument, "native-1", "variant-b")
	if !reflect.DeepEqual(*bareRow.Usage.Document, titleless) || bareRow.ModifiedAt != nil {
		t.Fatalf("titleless variant or its unparsed clock changed: %+v", bareRow)
	}
	recentRow := usageRow(t, rows, sourceID, UsageTypeRecent, "native-1", "")
	if !reflect.DeepEqual(*recentRow.Usage.Recent, recent) || recentRow.Usage.Recent.LocalPlaceholder != nil {
		t.Fatalf("recent payload changed: %+v", recentRow.Usage.Recent)
	}
	for i, want := range people {
		got := usageRow(t, rows, sourceID, UsageTypeCollaborator, want.ID, "")
		if !reflect.DeepEqual(*got.Usage.Collaborator, want) || got.ModifiedAt != nil {
			t.Fatalf("collaborator %d changed: %+v", i, got.Usage.Collaborator)
		}
	}
	// Clocks: only the record's own modification field is parsed into the modification time.
	modified := time.Date(2026, 10, 9, 12, 34, 56, 500000000, time.UTC)
	if document.ModifiedAt == nil || !document.ModifiedAt.Equal(modified) || document.ModifiedAt.Location() != time.UTC ||
		recentRow.ModifiedAt == nil || !recentRow.ModifiedAt.Equal(time.Date(2026, 10, 9, 12, 34, 56, 0, time.UTC)) ||
		usageRow(t, rows, sourceID, UsageTypeRecent, "native-0", "").ModifiedAt != nil {
		t.Fatalf("modification clocks: document %v, recent %v", document.ModifiedAt, recentRow.ModifiedAt)
	}
	usageOnly := func(filter DocumentFilter) int {
		filter.Application = UsageDocumentSource
		return len(usageRows(t, s, filter))
	}
	for name, window := range map[string][2]time.Time{
		"sharing":     {time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC), time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)},
		"access":      {time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC), time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)},
		"row changed": {time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC), time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC)},
		"created":     {time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)},
		"capture":     {base.Add(-time.Hour), base.Add(time.Hour)},
	} {
		if n := usageOnly(DocumentFilter{ModifiedSince: window[0], ModifiedUntil: window[1]}); n != 0 {
			t.Fatalf("a modification window matched %d usage rows through the %s clock", n, name)
		}
	}
	if n := usageOnly(DocumentFilter{ModifiedSince: modified.Add(-time.Hour), ModifiedUntil: modified.Add(time.Hour)}); n != 2 {
		t.Fatalf("native modification window matched %d usage rows, want the document and its recent row", n)
	}
	// Order: known modification first, then unknown; ties by title, source and the full native key.
	var order []string
	for _, row := range rows {
		order = append(order, row.Usage.Type+":"+usageNativeID(*row.Usage))
	}
	want := []string{"document:native-1", "recent:native-1", "collaborator:person-1", "collaborator:person-2", "collaborator:person-3", "document:native-1", "recent:native-0"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	// Selected metadata is searchable; other native strings are kept in the payload but not indexed.
	for query, hits := range map[string]int{
		"Synthetic complete": 1, "docx": 3, "Owner": 1, "native-1": 3, "unique-native-1": 1, "site-native-1": 2, "item-native-0": 1,
		"Alpha": 1, "alpha@example.invalid": 1, "19:synthetic-thread@thread.v2": 1, "latest-thread": 1, "Latest Sharer": 1, "One": 1,
		"Synthetic subject": 1, "Collaborator": 1, "person-1@example.invalid": 1, "Department": 1, "stated-person-1": 1,
		"aad-Alpha": 0, "ical-synthetic": 0, "attachment-synthetic": 0, "Synthetic meeting": 0, "working-native-1": 0,
	} {
		if got := usageOnly(DocumentFilter{Query: query}); got != hits {
			t.Fatalf("query %q found %d usage rows, want %d", query, got, hits)
		}
	}
	sources, err := s.DocumentSources(ctx)
	var usage KnowledgeSource
	for _, source := range sources {
		if source.Kind == UsageDocumentKind {
			usage = source
		}
	}
	if err != nil || len(sources) != 3 || usage.Coverage != "partial" || len(usage.Losses) != 4 || usage.Losses["onedrive_usage_recent_unmapped"] != 4 ||
		usage.Losses["onedrive_usage_document_orphan"] != 2 {
		t.Fatalf("usage source or its losses: %+v %v", sources, err)
	}
	var physical int
	if err := s.db.QueryRow("select physical_observations from document_capture_metadata where capture_id=?", attempt.ID).Scan(&physical); err != nil || physical != 17 {
		t.Fatalf("physical usage observations = %d, %v; want 7 rows and 10 lost", physical, err)
	}
	// The exact URL is an unresolved reference, here to itself and to an Office JSON row.
	detail := usageRelations(t, s, document.ID)
	variants := map[string]int{}
	for _, reference := range detail.References {
		if reference.AssociationState != "unresolved" || reference.Field != "url" || reference.URL != full.URL {
			t.Fatalf("reference resolved or mislabeled: %+v", reference)
		}
		variants[reference.Application]++
	}
	if detail.ReferencesTotal != 2 || variants[UsageDocumentSource] != 1 || variants["supplied"] != 1 || detail.Source.Kind != UsageDocumentKind {
		t.Fatalf("exact URL occurrences = %v (total %d)", variants, detail.ReferencesTotal)
	}
	if detail := usageRelations(t, s, bareRow.ID); detail.ReferencesTotal != 0 || len(detail.References) != 0 {
		t.Fatalf("a row with no URL gained references: %+v", detail.References)
	}
	if surface, application, err := s.DocumentLocatorInput(ctx, attempt.LocatorID); err != nil || surface != "" || application != UsageDocumentSource {
		t.Fatalf("usage binding = %q %q %v", surface, application, err)
	}
	for _, filter := range []DocumentFilter{{Surface: officedocuments.Shared}, {Application: "supplied"}, {Application: "registry"}} {
		for _, row := range usageRows(t, s, filter) {
			if row.Variant == UsageDocumentKind {
				t.Fatalf("a JSON or registry filter matched a usage row: %+v", filter)
			}
		}
	}
	// More related variants than the preview holds: the first 100 in native-key order and the full count.
	many := onedriveusage.Result{Recent: []onedriveusage.RecentObservation{usageRecent("native-many")}}
	for i := range 101 {
		many.Documents = append(many.Documents, usageDocument("native-many", "variant-"+strconv.Itoa(1000+i), "Synthetic variant"))
	}
	wide := beginUsage(t, s, "/fixture/Business_wide/Microsoft.FileUsageSync.db", base)
	admitUsage(t, s, wide, base, "complete", "wide-one", many)
	wideRows := usageRows(t, s, DocumentFilter{Query: "native-many"})
	preview := usageRelations(t, s, usageRow(t, wideRows, wideRows[0].SourceID, UsageTypeRecent, "native-many", "").ID)
	if preview.UsageRelationsTotal != 101 || len(preview.UsageRelations) != 100 || !preview.UsageRelationsTruncated ||
		preview.UsageRelations[0].Format != "variant-1000" || preview.UsageRelations[99].Format != "variant-1099" {
		t.Fatalf("relation preview: total %d, returned %d, truncated %t", preview.UsageRelationsTotal, len(preview.UsageRelations), preview.UsageRelationsTruncated)
	}
}

func TestUsageSnapshotAndMigrationRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "archive.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	jsonAttempt := beginDocument(t, s, base)
	admitDocument(t, s, jsonAttempt, base, "complete", "json-one", []officedocuments.Document{documentFixture("Kept JSON document", 0)})
	registry := beginRegistry(t, s, registryLocator, base)
	admitRegistry(t, s, registry, base, "complete", "registry-one", officeregistry.Result{Documents: []officeregistry.Observation{registryObservation(12, "Kept registry document")}})
	state := func(s *Store) string {
		var out string
		if err := s.db.QueryRow(`select group_concat(rowid||'|'||source_id||'|'||capture_id||'|'||representation_digest||'|'||ordinal||'|'||surface||'|'||application||'|'||title||'|'||url||'|'||web_url||'|'||coalesce(modified_at,'')||'|'||document_json||'|'||variant||'|'||coalesce(registry_json,'')||'|'||coalesce(opened_at,''),'\n')
||(select group_concat(rowid||title||metadata,'\n') from document_fts)
||(select group_concat(capture_id||':'||observation_rowid,',') from document_capture_members) from document_observations`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := state(s)
	// Back to schema 14: no usage payload, no native key, no relation table, no membership pointer.
	for _, statement := range []string{"alter table document_observations drop column usage_json",
		"alter table document_observations drop column native_key", "drop table document_usage_relations",
		"alter table document_capture_metadata drop column members_capture",
		"update schema_migrations set version=14"} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	must0(s.Close())
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := ro.DocumentSnapshot(ctx, DocumentFilter{Limit: 5})
	if err != nil || len(snapshot.Rows) != 2 || len(snapshot.Sources) != 2 || snapshot.Rows[0].Usage != nil {
		t.Fatalf("schema 14 read-only snapshot: %+v %v", snapshot, err)
	}
	for _, row := range snapshot.Rows {
		if detail, err := ro.DocumentObservation(ctx, row.ID); err != nil || detail.UsageRelationsTotal != 0 || detail.UsageRelationsCaptureID != 0 {
			t.Fatalf("schema 14 read-only detail: %+v %v", detail, err)
		}
	}
	if need, err := ro.NeedsUpgrade(ctx); err != nil || need {
		t.Fatalf("optional usage columns contaminated legacy freshness: %t %v", need, err)
	}
	if n := rowCount(t, ro, `select max(version) from schema_migrations`); n != 14 {
		t.Fatalf("read-only open migrated the archive to %d", n)
	}
	if n := rowCount(t, ro, `select count(*) from pragma_table_info('document_observations') where name in ('usage_json','native_key')`); n != 0 {
		t.Fatal("read-only open added usage columns")
	}
	must0(ro.Close())
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if n := rowCount(t, s, `select max(version) from schema_migrations`); n != SchemaVersion {
		t.Fatalf("schema %d, want %d", n, SchemaVersion)
	}
	if after := state(s); after != before {
		t.Fatal("migration changed existing rows, their index text or their membership")
	}
	if n := rowCount(t, s, `select count(*) from document_observations where usage_json is null and native_key=''`); n != 2 {
		t.Fatal("existing rows did not receive the empty usage defaults")
	}
	if n := rowCount(t, s, `select count(*) from document_capture_metadata where members_capture is null`); n != 2 {
		t.Fatal("existing captures do not own their membership after the upgrade")
	}
	// A partial earlier migration is repaired; a statement that cannot run rolls the others back.
	for _, statement := range []string{"alter table document_observations drop column native_key"} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if after := state(s); after != before {
		t.Fatal("partial repair changed stored rows")
	}
	for _, statement := range []string{"alter table document_observations drop column usage_json",
		"alter table document_observations drop column native_key", "alter table document_capture_metadata drop column variant",
		"drop table document_capture_metadata"} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrate(ctx); err == nil {
		t.Fatal("migration succeeded although one statement cannot run")
	}
	if n := rowCount(t, s, `select count(*) from pragma_table_info('document_observations') where name in ('usage_json','native_key')`); n != 0 {
		t.Fatalf("a failed migration left %d of its usage columns applied", n)
	}

	// Two handles: a usage capture published between a snapshot's row read and its coverage read
	// is not half seen, and the next command sees all of it.
	live := filepath.Join(t.TempDir(), "private", "live.db")
	a, err := Open(ctx, live)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	firstCapture := beginUsage(t, a, usageLocator, base)
	admitUsage(t, a, firstCapture, base, "complete", "first", onedriveusage.Result{
		Recent:    []onedriveusage.RecentObservation{usageRecent("native-1")},
		Documents: []onedriveusage.DocumentObservation{usageDocument("native-1", "variant-a", "Original")}})
	peer, err := Open(ctx, live)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var second KnowledgeAttempt
	reader := &documentPublicationReader{knowledgeReader: tx, publish: func() {
		second = beginUsage(t, peer, usageLocator, base.Add(time.Hour))
		admitUsage(t, peer, second, base.Add(time.Hour), "partial", "second", onedriveusage.Result{
			Recent:    []onedriveusage.RecentObservation{usageRecent("native-1")},
			Documents: []onedriveusage.DocumentObservation{usageDocument("native-1", "variant-a", "Newer")},
			Losses:    []onedriveusage.Loss{{Code: "onedrive_usage_recent_unmapped", Count: 1}}})
	}}
	held, err := readDocumentSnapshot(ctx, reader, DocumentFilter{})
	if err != nil || len(held.Rows) != 2 || len(held.Sources) != 1 || held.Sources[0].AdmittedID != firstCapture.ID ||
		held.Sources[0].Status != "complete" || held.Rows[0].Retained || held.Rows[1].Retained {
		t.Fatalf("publication crossed snapshot boundaries: %+v %v", held, err)
	}
	if second.ID == 0 {
		t.Fatal("separate publication did not occur between row and coverage reads")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	current, err := a.DocumentSnapshot(ctx, DocumentFilter{})
	if err != nil || len(current.Rows) != 3 || current.Sources[0].AdmittedID != second.ID || current.Sources[0].Status != "partial" {
		t.Fatalf("next command did not see committed publication: %+v %v", current, err)
	}
}
