package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/onedriveusage"
)

func TestUsageCopiedCaptureArchiveOnly(t *testing.T) {
	for _, pointer := range []string{"absent", "null"} {
		t.Run(pointer, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "copied-usage.db")
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			first := beginUsage(t, s, usageLocator, base)
			admitUsage(t, s, first, base, "complete", "copied-a", onedriveusage.Result{
				Recent:    []onedriveusage.RecentObservation{usageRecent("native")},
				Documents: []onedriveusage.DocumentObservation{usageDocument("native", "format", "Synthetic copied")}})
			copied := beginUsage(t, s, usageLocator, base.Add(time.Hour))
			admitUsage(t, s, copied, base.Add(time.Hour), "unchanged", "copied-a", onedriveusage.Result{})
			for _, statement := range []string{
				`insert into document_capture_members(capture_id,observation_rowid) select ` + itoa(int(copied.ID)) + `,observation_rowid from document_capture_members where capture_id=` + itoa(int(first.ID)),
				`insert into document_usage_relations(capture_id,recent_rowid,document_rowid) select ` + itoa(int(copied.ID)) + `,recent_rowid,document_rowid from document_usage_relations where capture_id=` + itoa(int(first.ID)),
				`update document_capture_metadata set members_capture=null where capture_id=` + itoa(int(copied.ID)),
			} {
				if _, err := s.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			want, err := s.DocumentSnapshot(ctx, DocumentFilter{})
			if err != nil || len(want.Rows) != 2 || len(want.Sources) != 1 {
				t.Fatalf("copied snapshot: %+v %v", want, err)
			}
			details := make([]DocumentObservationDetail, len(want.Rows))
			for i, row := range want.Rows {
				details[i] = usageRelations(t, s, row.ID)
				if row.Retained || details[i].UsageRelationsCaptureID != copied.ID || details[i].UsageRelationsTotal != 1 {
					t.Fatalf("copied ownership: %+v", details[i])
				}
			}
			if pointer == "absent" {
				if _, err := s.db.Exec(`alter table document_capture_metadata drop column members_capture`); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			ro, err := OpenReadOnly(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			var schemaBefore, schemaAfter string
			schemaSQL := `select group_concat(sql,';') from (select sql from sqlite_master order by type,name)`
			if err := ro.db.QueryRow(schemaSQL).Scan(&schemaBefore); err != nil {
				t.Fatal(err)
			}
			got, err := ro.DocumentSnapshot(ctx, DocumentFilter{})
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("archive-only copied snapshot: %+v %v; want %+v", got, err, want)
			}
			for i, row := range want.Rows {
				detail, err := ro.DocumentObservation(ctx, row.ID)
				if err != nil || !reflect.DeepEqual(detail, details[i]) {
					t.Fatalf("archive-only copied detail/relations: %+v %v; want %+v", detail, err, details[i])
				}
			}
			if err := ro.db.QueryRow(schemaSQL).Scan(&schemaAfter); err != nil || schemaBefore != schemaAfter {
				t.Fatalf("archive-only schema changed: %v", err)
			}
			if n := rowCount(t, ro, `select max(version) from schema_migrations`); n != 15 {
				t.Fatalf("schema version changed: %d", n)
			}
			if err := ro.Close(); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			afterInfo, statErr := os.Stat(path)
			if err != nil || statErr != nil || !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
				t.Fatalf("archive-only main bytes/mtime changed: %v %v", err, statErr)
			}
			s, err = Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			got, err = s.DocumentSnapshot(ctx, DocumentFilter{})
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("additive migration changed copied capture: %+v %v", got, err)
			}
			if n := rowCount(t, s, `select count(*) from pragma_table_info('document_capture_metadata') where name='members_capture'`); n != 1 {
				t.Fatalf("migration did not supply pointer: %d", n)
			}
			next := beginUsage(t, s, usageLocator, base.Add(2*time.Hour))
			admitUsage(t, s, next, base.Add(2*time.Hour), "unchanged", "copied-a", onedriveusage.Result{})
			for _, row := range usageRows(t, s, DocumentFilter{}) {
				detail := usageRelations(t, s, row.ID)
				if row.Retained || row.CaptureID != first.ID || detail.UsageRelationsCaptureID != copied.ID || detail.UsageRelationsTotal != 1 {
					t.Fatalf("migrated shared ownership: %+v", detail)
				}
			}
		})
	}
}

func TestUsageNativeComponentOrderBeforeLimit(t *testing.T) {
	s := newStore(t)
	prefix := strings.Repeat("q", 1100)
	attempt := beginUsage(t, s, usageLocator, base)
	// Every row has the same title and unknown modification time. Publication order is
	// deliberately different from literal Type, ID, Format order.
	admitUsage(t, s, attempt, base, "complete", "component-order", onedriveusage.Result{
		Recent: []onedriveusage.RecentObservation{{ID: "a"}},
		Documents: []onedriveusage.DocumentObservation{
			{ID: "a\x1fb", Format: "a"},
			{ID: "a", Format: prefix + "B"},
			{ID: "a", Format: "z"},
			{ID: "a", Format: prefix + "A"},
		},
		Collaborators: []onedriveusage.Collaborator{{ID: "a"}},
	})
	want := [][3]string{
		{"collaborator", "a", ""},
		{"document", "a", prefix + "A"},
		{"document", "a", prefix + "B"},
		{"document", "a", "z"},
		{"document", "a\x1fb", "a"},
		{"recent", "a", ""},
	}
	for _, limit := range []int{0, 2, 4} {
		rows, truncated, err := s.DocumentObservations(context.Background(), DocumentFilter{Limit: limit})
		count := len(want)
		if limit > 0 {
			count = limit
		}
		if err != nil || len(rows) != count || truncated != (limit > 0) {
			t.Fatalf("limit %d: rows=%d truncated=%v err=%v", limit, len(rows), truncated, err)
		}
		for i, row := range rows {
			id, format, _ := row.Usage.identity()
			if got := [3]string{row.Usage.Type, id, format}; got != want[i] {
				t.Fatalf("limit %d row %d: %q; want %q", limit, i, got, want[i])
			}
		}
	}
}
