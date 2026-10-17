package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/teamsdrafts"
)

func TestTeamsDraftsExcludedRunScope(t *testing.T) {
	s := draftStore(t)
	_, err := s.db.Exec(`insert into records(source,tenant_id,user_id,database,store,key_json,value_json,content_hash,first_seen_at,updated_at) values('Q|https_teams.microsoft.com_0','other','other','Teams:streams-replychain-manager:react-web-client:other:other:en','drafts','[]','{}','h','a','b')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`insert into sync_runs(source,status,started_at) values('Q|https_teams.microsoft.com_0',?,'a')`, strings.Repeat("x", 8*1024*1024+1))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.TeamsDrafts(context.Background(), TeamsDraftFilter{Account: &teamsdesktop.Account{TenantID: "tenant", UserID: "user"}, Limit: 50})
	if err != nil || len(r.Rows) != 1 || !r.Coverage.ScanComplete || r.Coverage.VisitedRows != 1 {
		t.Fatal(r, err)
	}
}
func TestTeamsDraftsAccountClockWithZeroBodies(t *testing.T) {
	s := newStore(t)
	_, err := s.db.Exec(`insert into sync_runs(source,status,started_at,finished_at,accounts_json) values('','ok','2026-01-01','2026-01-02','["tenant/user"]')`)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.TeamsDrafts(context.Background(), TeamsDraftFilter{Account: &teamsdesktop.Account{TenantID: "tenant", UserID: "user"}, Limit: 50})
	if err != nil || len(r.Rows) != 0 || len(r.Coverage.Clocks) != 1 || r.Coverage.Clocks[0].Scope != "tenant/user" {
		t.Fatal(r, err)
	}
}

func TestTeamsDraftsMetadataKeyValidation(t *testing.T) {
	s := draftStore(t)
	addMetadata(t, s, "{", "en", metadataJSON)
	a := draftSnapshot(t, s).Rows[0].Association
	if a.ScanComplete || a.MetadataState != "incomplete" {
		t.Fatal("invalid metadata key established association", a)
	}
}
func TestTeamsDraftsEnvelopeAndFilterBoundaries(t *testing.T) {
	v := make([]sql.NullString, 10)
	for i := range v {
		v[i] = sql.NullString{String: "x", Valid: true}
	}
	v[5].String = `"key"`
	v[9].Valid = false
	e, ok := draftEnvelope(v)
	if !ok || e.Source != "x" || e.RemovedAt != nil {
		t.Fatal(e, ok)
	}
	v[9] = sql.NullString{String: "removed", Valid: true}
	e, ok = draftEnvelope(v)
	if !ok || e.RemovedAt == nil || *e.RemovedAt != "removed" {
		t.Fatal(e, ok)
	}
	v[0].Valid = false
	if _, ok := draftEnvelope(v); ok {
		t.Fatal("missing source")
	}
	s := draftStore(t)
	for _, f := range []TeamsDraftFilter{{Limit: -1}, {ID: "bad"}, {BodyRevision: strings.Repeat("0", 64)}, {ID: "teams-draft:" + strings.Repeat("0", 64), BodyRevision: "bad"}} {
		if r, err := s.TeamsDrafts(context.Background(), f); err == nil || len(r.Rows) != 0 {
			t.Fatal(f, r, err)
		}
	}
	b, err := json.Marshal(TeamsDraftCoverage{SelectedAccount: &teamsdesktop.Account{TenantID: "tenant", UserID: "user"}})
	if err != nil || !strings.Contains(string(b), `"tenant_id":"tenant"`) {
		t.Fatal(string(b), err)
	}
	if _, err := json.Marshal(TeamsDraftCoverage{}); err != nil {
		t.Fatal(err)
	}
	ids, complete, err := draftRowIDs(context.Background(), s.db, "records", "1=1", nil, -1)
	if err != nil || complete || len(ids) != 0 {
		t.Fatal(ids, complete, err)
	}
}

func TestTeamsDraftsBodyLookupVariants(t *testing.T) {
	for _, value := range []any{nil, `{`} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			s := draftStore(t)
			if _, err := s.db.Exec(`update records set value_json=?`, value); err != nil {
				t.Fatal(err)
			}
			r := draftSnapshot(t, s)
			if r.Coverage.BodyScanComplete || r.Total != nil || len(r.Rows) != 0 {
				t.Fatal(r)
			}
			if _, err := s.TeamsDrafts(context.Background(), TeamsDraftFilter{ID: "teams-draft:" + strings.Repeat("0", 64)}); !errors.Is(err, ErrTeamsDraftScanIncomplete) {
				t.Fatal(err)
			}
		})
	}
	s := draftStore(t)
	b := draftSnapshot(t, s).Rows[0].Body
	for _, field := range []string{"creator", "type"} {
		_, err := s.db.Exec(`update records set value_json=json_set(?,?,'other'),removed_at='removed'`, draftJSON, "$.messageMap.member."+field)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.TeamsDrafts(context.Background(), TeamsDraftFilter{ID: b.ID, BodyRevision: strings.Repeat("0", 64)}); !errors.Is(err, ErrTeamsDraftUnknown) {
			t.Fatal(field, err)
		}
	}
	if _, err := s.db.Exec(`update records set value_json=?,removed_at=null,source='unsupported'`, draftJSON); err != nil {
		t.Fatal(err)
	}
	if r := draftSnapshot(t, s); len(r.Rows) != 0 || !r.Coverage.BodyScanComplete {
		t.Fatal(r)
	}
}
func TestTeamsDraftsAssociationAndLimits(t *testing.T) {
	s := draftStore(t)
	addMetadata(t, s, `"one"`, "en", metadataJSON)
	_, err := s.db.Exec(`insert into conversations(tenant_id,user_id,id,kind,title,team_id,updated_at) values('tenant','user','target','Space','title','team','a')`)
	if err != nil {
		t.Fatal(err)
	}
	a := draftSnapshot(t, s).Rows[0].Association
	if a.TargetState != "resolved" || a.Kind == nil || *a.Kind != "Space" {
		t.Fatal(a)
	}
	_, err = s.db.Exec(`update records set removed_at='historical' where store='drafts-metadata-items'`)
	if err != nil {
		t.Fatal(err)
	}
	if a := draftSnapshot(t, s).Rows[0].Association; a.MetadataState != "historical_only" || a.TargetState != "historical" {
		t.Fatal(a)
	}
	_, err = s.db.Exec(`update conversations set kind='Chat'`)
	if err != nil {
		t.Fatal(err)
	}
	if a := draftSnapshot(t, s).Rows[0].Association; a.MetadataState != "contradictory" || a.TargetState != "association_unknown" {
		t.Fatal(a)
	}
	_, err = s.db.Exec(`update records set value_json=json_set(value_json,'$.messageMap.member.properties.activity.sourceReplyChainId','one') where store='drafts'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`update records set value_json=json_set(value_json,'$.sourceReplyChainId','other') where store='drafts-metadata-items'`)
	if err != nil {
		t.Fatal(err)
	}
	if a := draftSnapshot(t, s).Rows[0].Association; a.MetadataState != "contradictory" {
		t.Fatal(a)
	}
	addDraftRow(t, s, "Q|https_teams.microsoft.com_0", draftDB, "drafts", `["wrapper","reply"]`, draftJSON)
	r, err := s.TeamsDrafts(context.Background(), TeamsDraftFilter{Limit: 1})
	if err != nil || !r.Truncated || r.Total == nil || *r.Total != 2 || len(r.Rows) != 1 {
		t.Fatal(r, err)
	}
	_, err = s.db.Exec(`update records set removed_at='removed' where store='drafts'`)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.TeamsDrafts(context.Background(), TeamsDraftFilter{IncludeRemoved: true, Limit: 50})
	if err != nil || len(r.Rows) != 2 {
		t.Fatal(r, err)
	}
	b := r.Rows[0].Body
	if _, err := s.TeamsDrafts(context.Background(), TeamsDraftFilter{ID: b.ID, IncludeRemoved: true, BodyRevision: b.BodyRevision}); err != nil {
		t.Fatal(err)
	}
}
func TestTeamsDraftsAdvisoryAndReceiptVariants(t *testing.T) {
	s := draftStore(t)
	addDraftRow(t, s, draftSource, "Teams:messaging-slice-manager:react-web-client:tenant:user:en", "drafts-internal-items", `"advisory"`, `{"count":7}`)
	if c := draftSnapshot(t, s).Coverage; c.AdvisoryState != "available_opaque_not_body_total" {
		t.Fatal(c)
	}
	for _, raw := range []string{`{`, `{"loss":-1}`, `{"loss":"wrong"}`} {
		_, err := s.db.Exec(`delete from sync_runs`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.db.Exec(`insert into sync_runs(source,status,started_at,omissions_json) values(?,'failed','a',?)`, draftSource, raw)
		if err != nil {
			t.Fatal(err)
		}
		if c := draftSnapshot(t, s).Coverage; c.LossesComplete {
			t.Fatal("invalid receipt hidden", c)
		}
	}
	for _, raw := range []any{nil, `{`, `["*"]`} {
		if _, err := s.db.Exec(`delete from sync_runs`); err != nil {
			t.Fatal(err)
		}
		_, err := s.db.Exec(`insert into sync_runs(source,status,started_at,accounts_json) values('','unchanged','a',?)`, raw)
		if err != nil {
			t.Fatal(err)
		}
		r := draftSnapshot(t, s)
		if raw == `["*"]` && len(r.Coverage.Clocks) != 1 {
			t.Fatal(r)
		}
	}
}

type draftCheckpointContext struct {
	context.Context
	cancel   context.CancelFunc
	function string
	at       int64
	calls    atomic.Int64
}

func (c *draftCheckpointContext) Err() error {
	pc, _, _, ok := goruntime.Caller(1)
	if ok && strings.HasSuffix(goruntime.FuncForPC(pc).Name(), c.function) && c.calls.Add(1) == c.at {
		c.cancel()
	}
	return c.Context.Err()
}
func TestTeamsDraftsCancellationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   int64
	}{{".(*draftBudget).text", 1}, {".(*draftBudget).text", 2}, {"/teamsdrafts.DecodeWrapper", 1}} {
		s := draftStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		c := &draftCheckpointContext{Context: ctx, cancel: cancel, function: tc.name, at: tc.at}
		r, err := s.TeamsDrafts(c, TeamsDraftFilter{Limit: 50})
		cancel()
		if !errors.Is(err, context.Canceled) || len(r.Rows) != 0 {
			t.Fatal(tc, r, err)
		}
	}
}
func TestTeamsDraftsInvalidEnvelopeScopes(t *testing.T) {
	s := draftStore(t)
	if _, err := s.db.Exec(`update records set key_json='{'`); err != nil {
		t.Fatal(err)
	}
	if r := draftSnapshot(t, s); r.Coverage.BodyScanComplete || len(r.Rows) != 0 {
		t.Fatal(r)
	}
	s = draftStore(t)
	addMetadata(t, s, `"wrong"`, "en", metadataJSON)
	if _, err := s.db.Exec(`update records set database='Teams:messaging-slice-manager:react-web-client:foreign:user:en' where store='drafts-metadata-items'`); err != nil {
		t.Fatal(err)
	}
	if a := draftSnapshot(t, s).Rows[0].Association; a.ScanComplete || a.MetadataState != "incomplete" {
		t.Fatal(a)
	}
}
func TestTeamsDraftsMemberAndAccountOrdering(t *testing.T) {
	s := draftStore(t)
	var wrapper map[string]any
	if err := json.Unmarshal([]byte(draftJSON), &wrapper); err != nil {
		t.Fatal(err)
	}
	members := wrapper["messageMap"].(map[string]any)
	members["another"] = members["member"]
	raw, err := json.Marshal(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update records set value_json=?`, string(raw)); err != nil {
		t.Fatal(err)
	}
	r, err := s.TeamsDrafts(context.Background(), TeamsDraftFilter{})
	if err != nil || len(r.Rows) != 2 || r.Rows[0].Body.MemberKey != "another" {
		t.Fatal(r, err)
	}
	_, err = s.db.Exec(`insert into records(source,tenant_id,user_id,database,store,key_json,value_json,content_hash,first_seen_at,updated_at) values(?,'tenant','user2','Teams:streams-replychain-manager:react-web-client:tenant:user2:en','drafts','["wrapper","reply"]',?,'h','a','b')`, draftSource, strings.ReplaceAll(draftJSON, "8:orgid:user", "8:orgid:user2"))
	if err != nil {
		t.Fatal(err)
	}
	r = draftSnapshot(t, s)
	if len(r.Rows) != 3 {
		t.Fatal(r)
	}
	_, err = s.db.Exec(`insert into records(source,tenant_id,user_id,database,store,key_json,value_json,content_hash,first_seen_at,updated_at) values(?,'tenant2','user','Teams:streams-replychain-manager:react-web-client:tenant2:user:en','drafts','["wrapper","reply"]',?,'h','a','b')`, draftSource, draftJSON)
	if err != nil {
		t.Fatal(err)
	}
	addDraftRow(t, s, draftSource, draftDB, "drafts", `["wrapper-two","reply"]`, strings.Replace(draftJSON, `"conversationId":"wrapper"`, `"conversationId":"wrapper-two"`, 1))
	r = draftSnapshot(t, s)
	if len(r.Rows) != 5 {
		t.Fatal(r)
	}
	want := [][4]string{
		{"tenant2", "user", `["wrapper","reply"]`, "member"},
		{"tenant", "user2", `["wrapper","reply"]`, "member"},
		{"tenant", "user", `["wrapper","reply"]`, "another"},
		{"tenant", "user", `["wrapper","reply"]`, "member"},
		{"tenant", "user", `["wrapper-two","reply"]`, "member"},
	}
	for i, row := range r.Rows {
		b := row.Body
		if got := [4]string{b.Envelope.TenantID, b.Envelope.UserID, b.Envelope.KeyJSON, b.MemberKey}; got != want[i] {
			t.Fatal("tuple order", i, got, want[i])
		}
	}
}

func TestTeamsDraftsConflictingLocator(t *testing.T) {
	id := "teams-draft:" + strings.Repeat("a", 64)
	first := TeamsDraftRow{Body: teamsdrafts.Body{ID: id, BodyRevision: strings.Repeat("b", 64), Envelope: teamsdrafts.Envelope{Source: draftSource}}}
	second := first
	second.Body.Envelope.Source = "Q|https_teams.microsoft.com_0"
	// Exercise the collision reducer without replacing the production SHA256.
	if _, err := selectTeamsDraft([]TeamsDraftRow{first, second}, TeamsDraftFilter{ID: id}, true); !errors.Is(err, ErrTeamsDraftAmbiguous) {
		t.Fatal(err)
	}
	for _, complete := range []bool{false, true} {
		_, err := selectTeamsDraft(nil, TeamsDraftFilter{ID: id}, complete)
		want := ErrTeamsDraftScanIncomplete
		if complete {
			want = ErrTeamsDraftUnknown
		}
		if !errors.Is(err, want) {
			t.Fatal(err, want)
		}
	}
	removed := "removed"
	first.Body.Envelope.RemovedAt = &removed
	if _, err := selectTeamsDraft([]TeamsDraftRow{first}, TeamsDraftFilter{ID: id, BodyRevision: "wrong"}, true); !errors.Is(err, ErrTeamsDraftRemoved) {
		t.Fatal(err)
	}
	first.Body.Envelope.RemovedAt = nil
	if _, err := selectTeamsDraft([]TeamsDraftRow{first}, TeamsDraftFilter{ID: id, BodyRevision: "wrong"}, true); !errors.Is(err, ErrTeamsDraftBodyRevisionChanged) {
		t.Fatal(err)
	}
	rows, err := selectTeamsDraft([]TeamsDraftRow{first, second}, TeamsDraftFilter{ID: "other"}, true)
	if err == nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	rows, err = selectTeamsDraft([]TeamsDraftRow{first}, TeamsDraftFilter{ID: id, BodyRevision: first.Body.BodyRevision}, true)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
}

func seedDraftFaults(t *testing.T, s *Store) {
	t.Helper()
	addDraftRow(t, s, draftSource, draftDB, "drafts", `["wrapper","reply"]`, draftJSON)
	addMetadata(t, s, `"one"`, "en", metadataJSON)
	addDraftRow(t, s, draftSource, "Teams:messaging-slice-manager:react-web-client:tenant:user:en", "drafts-internal-items", `"advisory"`, `{"count":1}`)
	_, err := s.db.Exec(`insert into conversations(tenant_id,user_id,id,kind,title,team_id,updated_at) values('tenant','user','target','Space','synthetic title','team','2026-01-01')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`insert into sync_runs(started_at,source,status,omissions_json) values('2026-01-01',?,'ok','{}')`, draftSource)
	if err != nil {
		t.Fatal(err)
	}
}
func TestTeamsDraftsReadFaults(t *testing.T) {
	for _, kind := range []string{"begin", "commit", "prepare", "query", "next", "rowsclose"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			seedDraftFaults(t, s)
			inj := injectFaults(t, s)
			sweepKind(t, s, inj, kind, true, func(ctx context.Context, s *Store) error {
				_, err := s.TeamsDrafts(ctx, TeamsDraftFilter{Limit: 50})
				return err
			}, nil)
		})
	}
}
func TestTeamsDraftsNullableFetchFaults(t *testing.T) {
	s := newStore(t)
	seedDraftFaults(t, s)
	inj := injectFaults(t, s)
	for at := 1; at < 100; at++ {
		inj.arm("scan", at)
		r, err := s.TeamsDrafts(context.Background(), TeamsDraftFilter{Limit: 50})
		if !inj.disarm() {
			if err != nil {
				t.Fatal(err)
			}
			return
		}
		if err == nil && r.Coverage.ScanComplete {
			t.Fatalf("NULL fault%d returned complete coverage: %+v", at, r)
		}
	}
	t.Fatal("scan sweep did not finish")
}

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
	if r.Coverage.LossesComplete {
		t.Fatal("incomplete metadata claimed all losses known")
	}
}

func TestTeamsDraftsRemovedOnlyZero(t *testing.T) {
	s := draftStore(t)
	if _, err := s.db.Exec(`update records set removed_at='2026-01-03'`); err != nil {
		t.Fatal(err)
	}
	r := draftSnapshot(t, s)
	if len(r.Rows) != 0 || r.Coverage.ScopeState != "removed_only" {
		t.Fatal(r)
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
	addDraftRow(t, s, draftSource, "Teams:messaging-slice-manager:react-web-client:tenant:user:en", "drafts-internal-items", `"advisory"`, `{"count":1}`)
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
