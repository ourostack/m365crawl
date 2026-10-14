package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLocalMissingAttemptAndRetainedCorruption(t *testing.T) {
	t.Run("missing attempt", func(t *testing.T) {
		s := newStore(t)
		beginLocal(t, s, "docx", base)
		if _, err := s.db.Exec(`delete from knowledge_attempts; update knowledge_locators set latest_attempt=null`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LocalTextSources(context.Background(), 0); err == nil {
			t.Fatal("missing unadmitted attempt")
		}
	})
	t.Run("unreadable retained body", func(t *testing.T) {
		s := newStore(t)
		c := localCapture(t, "", "")
		commitLocal(t, s, c, base)
		c.Result.State = "compound_unreadable"
		c.VerifiedKind = nil
		c.SourceByteDigest = strings.Repeat("a", 64)
		commitLocal(t, s, c, base.Add(time.Second))
		if _, err := s.db.Exec(`update local_text_source_state set last_good_observation_id=999`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LocalTextSources(context.Background(), 0); err == nil {
			t.Fatal("retained body corruption")
		}
	})
	t.Run("missing version", func(t *testing.T) {
		s := newStore(t)
		for _, table := range []string{"local_text_locator_metadata", "local_file_snapshots", "local_text_observations", "local_text_capture_members", "local_text_source_state", "local_text_attempt_metadata", "local_text_references", "local_text_fts", "schema_migrations"} {
			if _, err := s.db.Exec("drop table " + table); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.LocalTextSources(context.Background(), 0); err == nil {
			t.Fatal("unprovable older schema")
		}
	})
}
