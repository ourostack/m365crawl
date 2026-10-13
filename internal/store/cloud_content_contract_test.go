package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/sharepointcontent"
)

type cloudPublicationReader struct {
	knowledgeReader
	publish func()
}

func (r *cloudPublicationReader) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := r.knowledgeReader.QueryContext(ctx, query, args...)
	if err == nil && r.publish != nil && strings.Contains(query, "from cloud_observations o") {
		publish := r.publish
		r.publish = nil
		publish()
	}
	return rows, err
}
func TestCloudSnapshotCoherentDuringPeerPublication(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "archive.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	peer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	r := cloudRequest(t, "page", "")
	first := beginCloud(t, s, r, base)
	commitCloud(t, s, first, r, cloudResult("page", ""), base)
	old := cloudReader
	t.Cleanup(func() { cloudReader = old })
	var next KnowledgeAttempt
	cloudReader = func(tx *sql.Tx) knowledgeReader {
		return &cloudPublicationReader{knowledgeReader: tx, publish: func() {
			next = beginCloud(t, peer, r, base.Add(time.Hour))
			result := cloudResult("page", "")
			title := "new"
			result.File.Title = &title
			commitCloud(t, peer, next, r, result, base.Add(time.Hour))
		}}
	}
	snap, err := s.CloudSnapshot(ctx, CloudFilter{})
	if err != nil || next.ID == 0 || len(snap.Rows) != 1 || snap.Rows[0].Retained || snap.Sources[0].Source.AdmittedID != first.ID {
		t.Fatal("mixed snapshot")
	}
	cloudReader = old
	snap, err = s.CloudSnapshot(ctx, CloudFilter{})
	if err != nil || len(snap.Rows) != 2 || snap.Sources[0].Source.AdmittedID != next.ID {
		t.Fatal("next publication not seen")
	}
}

func TestCloudReadOnlySchema15DoesNotMigrate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "archive.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"cloud_locator_metadata", "cloud_observations", "cloud_capture_members", "cloud_capture_metadata", "cloud_source_body_state", "cloud_fts"} {
		if _, err := s.db.Exec("drop table " + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("update schema_migrations set version=15 where version=16"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path) //nolint:gosec // G304: exact synthetic archive under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ro.CloudSnapshot(ctx, CloudFilter{})
	if err != nil || len(snap.Rows) != 0 || len(snap.Sources) != 0 {
		t.Fatal("old schema optional content")
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path) //nolint:gosec // G304: exact synthetic archive under t.TempDir
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read-only migration/write")
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if version := rowCount(t, s, "select max(version) from schema_migrations"); version != 16 {
		t.Fatal("schema16 not sequential")
	}
}

func TestCloudHistoricalCorruptionAndSelectionGuards(t *testing.T) {
	for _, statement := range []string{
		"update cloud_observations set result_json='not-json' where rowid=1",
		"update cloud_observations set result_json='{}' where rowid=1",
		"update cloud_observations set representation_digest='wrong' where rowid=1",
		"update knowledge_locators set kind='wrong'",
	} {
		t.Run(statement, func(t *testing.T) {
			s := newStore(t)
			r := cloudRequest(t, "page", "")
			result := cloudResult("page", "")
			commitCloud(t, s, beginCloud(t, s, r, base), r, result, base)
			title := "new"
			result.File.Title = &title
			commitCloud(t, s, beginCloud(t, s, r, base.Add(time.Hour)), r, result, base.Add(time.Hour))
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CloudSnapshot(context.Background(), CloudFilter{}); err == nil {
				t.Fatal("historical corruption accepted")
			}
		})
	}
	for _, bad := range []string{"json", "typed", "nonbody", "boundary"} {
		t.Run(bad, func(t *testing.T) {
			s := newStore(t)
			r := cloudRequest(t, "stream", "")
			body := cloudResult("stream", "T")
			commitCloud(t, s, beginCloud(t, s, r, base), r, body, base)
			commitCloud(t, s, beginCloud(t, s, r, base.Add(time.Hour)), r, cloudResult("stream", ""), base.Add(time.Hour))
			var value string
			switch bad {
			case "json":
				value = "not-json"
			case "typed":
				value = "{}"
			case "nonbody":
				body = cloudResult("stream", "")
				encoded, _ := json.Marshal(body)
				value = string(encoded)
			case "boundary":
				body.SourceAccount.LoginName = "changed"
				encoded, _ := json.Marshal(body)
				value = string(encoded)
			}
			if _, err := s.db.Exec("update cloud_observations set result_json=? where rowid=1", value); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CloudSnapshot(context.Background(), CloudFilter{Query: "absent"}); err == nil {
				t.Fatal("corrupt last-good body accepted at zero hits")
			}
		})
	}
	s := newStore(t)
	r := cloudRequest(t, "page", "")
	result := cloudResult("page", "")
	result.File.ModifiedRaw = new(string)
	*result.File.ModifiedRaw = "unknown"
	result.Partial = true
	result.Losses = []sharepointcontent.Loss{{Code: "modified_time_unmapped", Count: 1}}
	commitCloud(t, s, beginCloud(t, s, r, base), r, result, base)
	if snap, err := s.CloudSnapshot(context.Background(), CloudFilter{ModifiedSince: base.Add(-time.Hour)}); err != nil || len(snap.Rows) != 0 {
		t.Fatal("unknown native clock admitted to bound")
	}
	if text := CloudText(sharepointcontent.Result{Transcript: &sharepointcontent.Transcript{Entries: []sharepointcontent.TranscriptEntry{{Text: "one"}, {Text: "one"}}}}); text != "one\none" {
		t.Fatal("native entry occurrences collapsed")
	}
}
