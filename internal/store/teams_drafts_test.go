package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

const draftDB = "Teams:streams-replychain-manager:react-web-client:tenant:user:en"
const draftSource = "P|https_teams.microsoft.com_0"
const draftJSON = `{"conversationId":"wrapper","replyChainId":"reply","messageMap":{"member":{"id":"different-id","creator":"8:orgid:user","type":"Message","draftType":"RegularDraft","threadType":"streamofdrafts","messageType":"RichText/Html","contentType":"Text","state":0,"isSentByCurrentUser":true,"content":"<p>synthetic body</p>","clientMessageId":"client","lieType":"Send","sendPipelineStatus":{"$undefined":true},"sendPipelineOAMState":{"$undefined":true},"streamingMetadata":{"$undefined":true},"skypeeditedid":{"$undefined":true},"originalNonLieMessage":{"$undefined":true},"properties":{"sendAt":{"$undefined":true},"activity":{"activityType":"Draft","activitySubtype":"Draft","sourceUserId":"8:orgid:user","sourceThreadId":"target","sourceMessageId":"message"}},"draftDetails":{"clientDraftProperties":{"scheduledSendTime":null,"isForwardedMessage":false,"forwardPrivateReply":false,"forwardAppIdsToInstall":"[]","forwardMessageIds":"[]","forwardMessageRecipients":"[]","forwardMessageCardContents":"null","forwardMessageCardsCount":null,"forwardMessageOriginalConvId":null,"forwardTeamId":null}}}}}`

func addDraftRow(t *testing.T, s *Store, source, db, store, key, value string) {
	t.Helper()
	_, err := s.db.Exec(`insert into records(source,tenant_id,user_id,database,store,key_json,value_json,content_hash,first_seen_at,updated_at) values(?,?,?,?,?,?,?,'unused','2026-01-01','2026-01-02')`, source, "tenant", "user", db, store, key, value)
	if err != nil {
		t.Fatal(err)
	}
}
func draftStore(t *testing.T) *Store {
	t.Helper()
	s := newStore(t)
	addDraftRow(t, s, draftSource, draftDB, "drafts", `["wrapper","reply"]`, draftJSON)
	return s
}
func draftSnapshot(t *testing.T, s *Store) TeamsDraftSnapshot {
	t.Helper()
	r, err := s.TeamsDrafts(context.Background(), TeamsDraftFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func addMetadata(t *testing.T, s *Store, key, locale, value string) {
	t.Helper()
	addDraftRow(t, s, draftSource, "Teams:messaging-slice-manager:react-web-client:tenant:user:"+locale, "drafts-metadata-items", key, value)
}

const metadataJSON = `{"sourceThreadId":"target","sourceMessageId":"message","clientMessageId":"client","conversationType":"Space"}`

func TestTeamsDraftsCoverageAndOrdering(t *testing.T) {
	s := draftStore(t)
	r := draftSnapshot(t, s)
	if len(r.Rows) != 1 || r.Total == nil || *r.Total != 1 || !r.Coverage.ScanComplete || r.Rows[0].Body.Text == nil || *r.Rows[0].Body.Text != "synthetic body" {
		t.Fatalf("%+v", r)
	}
	r, err := s.TeamsDrafts(context.Background(), TeamsDraftFilter{Account: &teamsdesktop.Account{TenantID: "other", UserID: "user"}, Limit: 50})
	if err != nil || len(r.Rows) != 0 || r.Total == nil || *r.Total != 0 {
		t.Fatal(r, err)
	}
}
func TestTeamsDraftsGuardScope(t *testing.T) {
	s := draftStore(t)
	b := draftSnapshot(t, s).Rows[0].Body
	addMetadata(t, s, `"one"`, "fr", metadataJSON)
	r, err := s.TeamsDrafts(context.Background(), TeamsDraftFilter{ID: b.ID, BodyRevision: b.BodyRevision})
	if err != nil || len(r.Rows) != 1 || r.Rows[0].Body.BodyRevision != b.BodyRevision {
		t.Fatal(r, err)
	}
	if _, err = s.db.Exec(`update records set value_json=replace(value_json,'synthetic body','changed body') where store='drafts'`); err != nil {
		t.Fatal(err)
	}
	r, err = s.TeamsDrafts(context.Background(), TeamsDraftFilter{ID: b.ID, BodyRevision: b.BodyRevision})
	if !errors.Is(err, ErrTeamsDraftBodyRevisionChanged) || len(r.Rows) != 0 {
		t.Fatal(r, err)
	}
}
func TestTeamsDraftsErrorPrecedence(t *testing.T) {
	s := draftStore(t)
	b := draftSnapshot(t, s).Rows[0].Body
	if _, err := s.db.Exec(`update records set removed_at='2026-01-03'`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		f    TeamsDraftFilter
		want error
	}{
		{TeamsDraftFilter{ID: b.ID, BodyRevision: strings.Repeat("0", 64)}, ErrTeamsDraftRemoved},
		{TeamsDraftFilter{ID: b.ID, Account: &teamsdesktop.Account{TenantID: "other", UserID: "user"}}, ErrTeamsDraftUnknown},
		{TeamsDraftFilter{ID: "teams-draft:" + strings.Repeat("0", 64)}, ErrTeamsDraftUnknown},
	} {
		r, err := s.TeamsDrafts(context.Background(), tc.f)
		if !errors.Is(err, tc.want) || len(r.Rows) != 0 {
			t.Fatal(r, err, tc.want)
		}
	}
}
func TestTeamsDraftsRelevantUniverseExhaustion(t *testing.T) {
	s := draftStore(t)
	addMetadata(t, s, `"one"`, "en", metadataJSON)
	addMetadata(t, s, `"two"`, "fr", metadataJSON)
	r := draftSnapshot(t, s)
	a := r.Rows[0].Association
	if a.MetadataState != "ambiguous" || !a.ScanComplete || a.ObservedMatchesLowerBound != 2 {
		t.Fatal(a)
	}
	addMetadata(t, s, `"oversize"`, "de", strings.Repeat("x", 8*1024*1024+1))
	a = draftSnapshot(t, s).Rows[0].Association
	if a.MetadataState != "incomplete" || a.TargetState != "association_unknown" || a.ScanComplete {
		t.Fatal(a)
	}
}
func TestTeamsDraftsIndependentCompleteness(t *testing.T) {
	s := draftStore(t)
	b := draftSnapshot(t, s).Rows[0].Body
	addMetadata(t, s, `"bad"`, "en", `{`)
	r, err := s.TeamsDrafts(context.Background(), TeamsDraftFilter{ID: b.ID, BodyRevision: b.BodyRevision})
	if err != nil || len(r.Rows) != 1 || r.Rows[0].Association.ScanComplete || r.Rows[0].Body.BodyRevision == "" {
		t.Fatal(r, err)
	}
}

type draftTrackingReader struct {
	knowledgeReader
	payloads int
}

func (r *draftTrackingReader) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	if strings.HasPrefix(q, "select source,") {
		r.payloads++
	}
	return r.knowledgeReader.QueryRowContext(ctx, q, args...)
}
func TestTeamsDraftsDescriptorBeforeMaterialization(t *testing.T) {
	s := draftStore(t)
	if _, err := s.db.Exec(`update records set value_json=?`, strings.Repeat("x", 8*1024*1024+1)); err != nil {
		t.Fatal(err)
	}
	old := teamsDraftReader
	defer func() { teamsDraftReader = old }()
	var tracker *draftTrackingReader
	teamsDraftReader = func(tx *sql.Tx) knowledgeReader { tracker = &draftTrackingReader{knowledgeReader: tx}; return tracker }
	r := draftSnapshot(t, s)
	if tracker.payloads != 0 || r.Coverage.ScanComplete || len(r.Rows) != 0 || r.Total != nil {
		t.Fatal(tracker.payloads, r)
	}
}
func TestTeamsDraftsAuxiliaryPrecharge(t *testing.T) {
	s := draftStore(t)
	addMetadata(t, s, `"one"`, "en", metadataJSON)
	_, err := s.db.Exec(`insert into conversations(tenant_id,user_id,id,kind,title,team_id,updated_at) values('tenant','user','target','Space',?,'team','2026-01-01')`, strings.Repeat("x", 8*1024*1024+1))
	if err != nil {
		t.Fatal(err)
	}
	a := draftSnapshot(t, s).Rows[0].Association
	if a.ConversationComplete || a.TargetState == "resolved" {
		t.Fatal(a)
	}
}
func TestTeamsDraftsDescriptorFetchSnapshot(t *testing.T) {
	t.Run("publish between descriptor and fetch", func(t *testing.T) {
		s := draftStore(t)
		reader, err := OpenReadOnly(context.Background(), s.cs.Path())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = reader.Close() }()
		old := teamsDraftReader
		defer func() { teamsDraftReader = old }()
		changed := false
		teamsDraftReader = func(tx *sql.Tx) knowledgeReader {
			return &draftBarrierReader{knowledgeReader: tx, before: func(q string) {
				if strings.HasPrefix(q, "select source,") && !changed {
					changed = true
					if _, err := s.db.Exec(`update records set value_json=replace(value_json,'synthetic body','NEW body')`); err != nil {
						t.Fatal(err)
					}
				}
			}}
		}
		r := draftSnapshot(t, reader)
		if !changed || *r.Rows[0].Body.Text != "synthetic body" {
			t.Fatal("mixed snapshot", r)
		}
		teamsDraftReader = old
		if got := *draftSnapshot(t, s).Rows[0].Body.Text; got != "NEW body" {
			t.Fatal(got)
		}
	})
	s := draftStore(t)
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	r, err := s.TeamsDrafts(ctx, TeamsDraftFilter{Limit: 50})
	if !errors.Is(err, context.Canceled) || len(r.Rows) != 0 {
		t.Fatal(r, err)
	}
}

type draftBarrierReader struct {
	knowledgeReader
	before func(string)
}

func (r *draftBarrierReader) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	r.before(q)
	return r.knowledgeReader.QueryRowContext(ctx, q, args...)
}

func TestTeamsDraftsReceiptAndAdvisoryBounds(t *testing.T) {
	s := draftStore(t)
	_, err := s.db.Exec(`insert into sync_runs(started_at,finished_at,source,status,omissions_json) values('2026-01-01','2026-01-02',?,'ok_with_omissions','{"v8_host_object":190}')`, draftSource)
	if err != nil {
		t.Fatal(err)
	}
	r := draftSnapshot(t, s)
	if len(r.Coverage.Clocks) == 0 {
		t.Fatal("available source clocks omitted")
	}
	found := false
	for _, loss := range r.Coverage.ObservedLossesLowerBound {
		if loss.Code == "v8_host_object" && loss.Category == "source_wide_not_draft_store" && loss.Count == 190 {
			found = true
		}
	}
	if !found {
		t.Fatal("source omissions not independently qualified", r.Coverage)
	}
	addDraftRow(t, s, draftSource, "Teams:messaging-slice-manager:react-web-client:tenant:user:en", "drafts-internal-items", `"advisory"`, strings.Repeat("x", 8*1024*1024+1))
	r = draftSnapshot(t, s)
	if len(r.Rows) != 1 || r.Coverage.AdvisoryState != "incomplete" || !r.Coverage.BodyScanComplete {
		t.Fatal(r)
	}
}

func TestTeamsDraftsInputBudgetBoundaries(t *testing.T) {
	s := draftStore(t)
	var id int64
	if err := s.db.QueryRow(`select rowid from records`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		before int64
		want   bool
	}{{draftTotalCap - 7, true}, {draftTotalCap - 6, false}} {
		if _, err := s.db.Exec(`update records set value_json=?`, "é\x00🌱"); err != nil {
			t.Fatal(err)
		}
		b := &draftBudget{bytes: tc.before}
		v, ok, err := b.text(context.Background(), s.db, "records", []string{"value_json"}, id)
		if err != nil || ok != tc.want || ok && v[0].String != "é\x00🌱" {
			t.Fatal(v, ok, err)
		}
	}
	b := &draftBudget{rows: draftRowCap}
	if _, ok, err := b.text(context.Background(), s.db, "records", []string{"value_json"}, id); err != nil || ok {
		t.Fatal(ok, err)
	}
	if _, err := s.db.Exec(`update records set value_json=123`); err != nil {
		t.Fatal(err)
	}
	// TEXT affinity converts numbers; BLOB is an actually different SQL representation.
	if _, err := s.db.Exec(`update records set value_json=CAST('bad' AS BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := (&draftBudget{}).text(context.Background(), s.db, "records", []string{"value_json"}, id); err != nil || ok {
		t.Fatal(ok, err)
	}
}

func TestTeamsDraftsSecondMatchBeyondRowCap(t *testing.T) {
	s := draftStore(t)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`insert into records(source,tenant_id,user_id,database,store,key_json,value_json,content_hash,first_seen_at,updated_at) values(?,'tenant','user','Teams:messaging-slice-manager:react-web-client:tenant:user:en','drafts-metadata-items',?,?,'h','a','b')`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4096; i++ {
		v := `{"sourceThreadId":"other","sourceMessageId":"other","clientMessageId":"other"}`
		if i == 0 || i == 4095 {
			v = metadataJSON
		}
		if _, err := stmt.Exec(draftSource, fmt.Sprintf(`"%d"`, i), v); err != nil {
			t.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	a := draftSnapshot(t, s).Rows[0].Association
	if a.MetadataState != "incomplete" || a.ScanComplete || a.ObservedMatchesLowerBound != 1 || a.TargetState != "association_unknown" {
		t.Fatal(a)
	}
}
