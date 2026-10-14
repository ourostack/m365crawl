package store

import (
	"context"
	"testing"
)

func TestEngageCompleteReadIntegrity(t *testing.T) {
	for _, sql := range []string{
		`update engage_sessions set result_json='{}'`,
		`update engage_sessions set representation_digest='corrupt'`,
		`update engage_sessions set acquired_at='invalid'`,
		`update engage_occurrences set native_thread_id='corrupt'`,
		`update engage_occurrences set starter_modified_at='corrupt'`,
		`delete from engage_fts where rowid=1`,
		`update engage_fts set text='corrupt' where rowid=1`,
		`insert into engage_fts(rowid,title,text,metadata) values(999,'','','')`,
		`delete from engage_capture_members where capture_id=2`,
		`update knowledge_attempts set losses_json='{}' where id=2`,
		`update knowledge_attempts set coverage='complete' where id=2`,
		`update knowledge_sources set complete_attempt=1`,
		`update engage_source_state set current_session_id=999`,
		`update knowledge_locators set latest_attempt=1`,
		`update engage_source_metadata set account_json='{}'`,
		`update engage_locator_metadata set target_url='other'`,
		`update knowledge_attempts set finished_at='invalid' where id=3`,
		`update knowledge_attempts set started_at='invalid' where id=2`,
		`update knowledge_attempts set acquisition_signature='invented' where id=2`,
		`update knowledge_attempts set finished_at=null where id=2`,
		`update engage_sessions set capture_id=2`,
		`update engage_sessions set capture_id=999`,
		`delete from engage_occurrences where ordinal=1`,
		`insert into engage_occurrences(source_id,session_id,ordinal,native_thread_id,native_starter_id) values(1,1,2,'extra','extra')`,
		`update engage_sessions set captured_at='0001-01-01T00:00:00.000000000Z'`,
		`update engage_sessions set captured_at='2020-01-01T00:00:00.000000000Z'`,
		`update knowledge_sources set source_observed_at='invented'`,
		`update knowledge_sources set admitted_attempt=999`,
		`update engage_occurrences set ordinal=3 where ordinal=1`,
	} {
		t.Run(sql, func(t *testing.T) {
			s := newStore(t)
			r := engageResult("A")
			commitEngage(t, s, r, base)
			commitEngage(t, s, r, base)
			commitEngage(t, s, r, base)
			if _, err := s.db.Exec(sql); err != nil {
				t.Fatal(err)
			}

			for _, filter := range []EngageFilter{{Query: "absent", Limit: 1}, {SourceID: 999}} {
				if _, err := s.EngageSnapshot(context.Background(), filter); err == nil {
					t.Fatal("corruption hidden by filter")
				}
			}
			if _, err := s.EngageSources(context.Background()); err == nil {
				t.Fatal("sources hid corruption")
			}
			if _, err := s.EngageDetail(context.Background(), 999); err == nil {
				t.Fatal("detail hid corruption")
			}
			a, beginErr := s.BeginEngageAttempt(context.Background(), KnowledgeInput{Kind: EngageKind, Platform: "browser", Locator: EngageHomeURL, At: base})
			if beginErr == nil && s.CommitEngageCapture(context.Background(), a, EngageCapture{r, base, base}) == nil {
				t.Fatal("reuse repaired corruption")
			}
		})
	}
}

func TestEngageReadOnlyOptionalAbsence(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, table := range []string{"engage_locator_metadata", "engage_source_metadata", "engage_sessions", "engage_occurrences", "engage_capture_members", "engage_source_state", "engage_fts"} {
		if _, err := s.db.Exec("drop table " + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.EngageSources(ctx); err == nil {
		t.Fatal("current schema namespace loss hidden")
	}
	if _, err := s.db.Exec(`update schema_migrations set version=17`); err != nil {
		t.Fatal(err)
	}
	snap, err := s.EngageSources(ctx)
	if err != nil || len(snap.Rows) != 0 {
		t.Fatal(err)
	}
}
