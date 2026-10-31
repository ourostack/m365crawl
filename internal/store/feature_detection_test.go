package store

import (
	"context"
	"strings"
	"testing"
)

// A recorded version only rules a feature out. An archive recorded at or above a feature's number
// that has none of its tables (numbers were reordered between parallel changes) reads as not
// having the feature, not as broken; some but not all of its tables is still an error (#306).
func TestFeatureDetectionTrustsTablesNotTheRecordedVersion(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		drop   string
		has    func(*Store) (bool, error)
		broken string
	}{
		{"directed writes", `drop table directed_write_drafts; drop table directed_write_events; drop table directed_writes`,
			func(s *Store) (bool, error) { return hasDirectedWrites(ctx, s.db) }, `drop table directed_write_events`},
		{"directed drafts", `drop table directed_write_drafts`,
			func(s *Store) (bool, error) { return hasDirectedDrafts(ctx, s.db) }, ``},
		{"WorkIQ documents", `drop table workiq_document_fts; drop table workiq_document_bodies; drop table workiq_document_checks; drop table workiq_documents`,
			func(s *Store) (bool, error) { return hasWorkIQDocuments(ctx, s.db) }, `drop table workiq_document_bodies`},
	} {
		s := openTest(t)
		if has, err := tc.has(s); err != nil || !has {
			t.Fatalf("%s: a current archive = %v, %v", tc.name, has, err)
		}
		if tc.broken != "" {
			broken := openTest(t)
			broken.qExec(t, tc.broken)
			if _, err := tc.has(broken); err == nil || !strings.Contains(err.Error(), "incomplete") {
				t.Errorf("%s: some tables missing = %v", tc.name, err)
			}
		}
		s.qExec(t, tc.drop)
		if has, err := tc.has(s); err != nil || has {
			t.Errorf("%s: recorded version, no tables = %v, %v", tc.name, has, err)
		}
	}
}
