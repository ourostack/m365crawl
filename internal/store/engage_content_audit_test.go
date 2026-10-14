package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEngageCanonicalBodyTamper(t *testing.T) {
	for _, name := range []string{"invalid-result", "account", "missing-occurrence", "missing-session", "missing-original", "stale"} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			r := engageResult("A")
			commitEngage(t, s, r, base)
			switch name {
			case "invalid-result", "account":
				if name == "invalid-result" {
					r.Complete = true
				} else {
					r.Account.UserID = "B"
				}
				b, _ := json.Marshal(r)
				if _, err := s.db.Exec(`update engage_sessions set result_json=?,representation_digest=?`, string(b), engageDigest(1, string(b))); err != nil {
					t.Fatal(err)
				}
			case "missing-occurrence":
				if _, err := s.db.Exec(`delete from engage_fts where rowid=2; delete from engage_occurrences where rowid=2`); err != nil {
					t.Fatal(err)
				}
			case "missing-session":
				if _, err := s.db.Exec(`update engage_source_state set current_session_id=2; update engage_capture_members set session_id=2; delete from engage_fts; delete from engage_occurrences; delete from engage_sessions`); err != nil {
					t.Fatal(err)
				}
			case "missing-original":
				if _, err := s.db.Exec(`update engage_sessions set capture_id=0`); err != nil {
					t.Fatal(err)
				}
			case "stale":
				a := beginEngage(t, s, base)
				if err := s.RecordEngageFailure(context.Background(), a, EngageFailure{"failed", "engage_unavailable", base}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`update knowledge_attempts set reason='stale_attempt',coverage='unknown' where id=2`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.EngageSources(context.Background()); err == nil {
				t.Fatal("canonical/session corruption hidden")
			}
		})
	}
}

func TestEngageAuditMemberClockRefusal(t *testing.T) {
	s := newStore(t)
	r := engageResult("A")
	a := commitEngage(t, s, r, base)
	session := EngageSession{ID: 1, SourceID: 1, CaptureID: a.ID, RepresentationDigest: engageDigest(1, engageJSON(r)), Qualification: EngageQualification, Result: r, AcquiredAt: base, CapturedAt: base}
	if _, err := s.db.Exec(`update knowledge_attempts set finished_at='invalid'`); err != nil {
		t.Fatal(err)
	}
	if err := auditEngageMembers(context.Background(), s.db, session, a.LocatorID); err == nil {
		t.Fatal("invalid member clock accepted")
	}
}
