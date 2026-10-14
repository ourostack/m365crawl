package cli

import (
	"context"
	"database/sql"
	"io"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/sourcecapture"
)

func TestLocalCaptureCancellationAndArchiveFaults(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("native synthetic capture")
	}
	t.Run("gate-cancel", func(t *testing.T) {
		e := cloudCLIEnv(t)
		path := localCLIFile(t, e)
		release, err := sourcecapture.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		rt := &runtime{ctx: ctx, g: &Globals{}, dbPath: e.db, stdout: io.Discard}
		if err := rt.captureLocalText(officecapture.TextInput{Path: path, RequestedKind: "docx"}, "explicit", nil); err == nil {
			t.Fatal("cancelled gated capture")
		}
	})
	for _, tc := range []struct {
		name, trigger string
		missing       bool
	}{
		{"begin", `create trigger local_fault before insert on knowledge_attempts begin select raise(abort,'synthetic begin'); end`, false},
		{"commit", `create trigger local_fault before insert on local_text_observations begin select raise(abort,'synthetic commit'); end`, false},
		{"record", `create trigger local_fault before update on knowledge_attempts begin select raise(abort,'synthetic record'); end`, true},
		{"snapshot", `create trigger local_fault after update of finished_at on knowledge_attempts when new.source_id is not null begin update local_text_observations set representation_digest='wrong'; end`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := cloudCLIEnv(t)
			path := localCLIFile(t, e)
			db, err := sql.Open("sqlite", e.db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.trigger); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if tc.missing {
				path += ".missing"
			}
			if code, _, _ := e.run("content", "capture", "--file", path, "--kind", "docx"); code == 0 {
				t.Fatal("archive/capture fault success")
			}
		})
	}
	e := cloudCLIEnv(t)
	rt := &runtime{ctx: context.Background(), g: &Globals{}, dbPath: e.db, stdout: io.Discard}
	if err := rt.captureLocalText(officecapture.TextInput{}, "explicit", nil); err == nil {
		t.Fatal("invalid runtime input")
	}
}
