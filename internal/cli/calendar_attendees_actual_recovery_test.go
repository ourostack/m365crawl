package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestAttendeeRealSourceIDMissingRootIsExplicitlyUnavailable(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "Main", "HxStore.hxd")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("synthetic-not-opened"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	commit := func(origin string) store.CalendarResult {
		t.Helper()
		result, err := s.CommitOutlook(ctx, store.OutlookBatch{Account: "outlook/Main", Origin: origin, At: at, Zone: time.UTC, Stamp: "synthetic"},
			func(store.CalendarResult) store.Run {
				return store.Run{StartedAt: at, FinishedAt: at, Source: "outlook|Main", Status: "ok", Fingerprint: "synthetic"}
			})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	cal := commit(path)
	a, err := s.BeginAttendeeAttempt(ctx, store.KnowledgeInput{Kind: store.AttendeeKind, Locator: path, Platform: "synthetic", At: at},
		store.AttendeeRequest{SourceID: 1})
	if err != nil {
		t.Fatal(err)
	}
	model, census, err := outlookmeetingattendees.Build(ctx, outlookattendees.Result{}, outlookcal.AttendeeSelections{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitAttendeeCapture(ctx, a, store.AttendeeCapture{Evidence: model, Census: census, InputDigest: strings.Repeat("b", 64),
		CalendarReadID: cal.OutlookReadID, AcquiredAt: at, AdmittedAt: at}); err != nil {
		t.Fatal(err)
	}
	commit("")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := e.run("calendar", "attendees", "sync", "--source-id", "1")
	if code == 0 {
		t.Fatal("rootless history qualified a source")
	}
	fix := errorOf(t, stderr)["fix"].(string)
	if !strings.Contains(fix, "No original bootstrap root is available") || !strings.Contains(fix, e.db) ||
		strings.Contains(fix, "sync --outlook-root") {
		t.Fatal("missing validated root must be a bounded explicit inability, not inferred/default discovery or dead-end retry", fix)
	}
}

func TestAttendeeRealSourceIDRecoveryCommandsExecute(t *testing.T) {
	e := newEnv(t)
	e.db = filepath.Join(t.TempDir(), "custom 'archive", "evidence.db")
	root := filepath.Join(t.TempDir(), "saved 'root")
	path := filepath.Join(root, "Main", "HxStore.hxd")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(root, "Main", "HxStore.hxd")
	native := hxbuild.NewObject(0x71, 0xb8, 184)
	native.PutU32(20, 11)
	native.PutU32(32, 3)
	native.PutU64(112, 42)
	native.AppendString("Native")
	native.PutU32(156, 14)
	start := time.Date(2031, 3, 5, 9, 30, 0, 0, time.UTC)
	builder := hxbuild.New(hxbuild.Options{})
	builder.Block(hxbuild.FramedPayload(hxbuild.Head(15),
		hxbuild.NewEvent(hxbuild.EventSpec{ID: hxbuild.GlobalObjectID(0, 0, 0, "saved-request"), SeriesKey: 1, Stamp: 1, DetailKey: 3,
			Start: start, End: start.Add(time.Hour), LastModified: start.Add(-time.Hour), Subject: "Synthetic request transport", ZoneID: 2, ZoneName: "UTC"}),
		hxbuild.NewDetail(hxbuild.DetailSpec{Key: 3, SeriesKey: 1}), native))
	if err := os.WriteFile(path, builder.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := syncer.SyncAttendees(ctx, syncer.AttendeeOptions{DBPath: e.db, Root: root, Explicit: true, Discover: true})
	if err != nil || first.Captured != 1 {
		t.Fatal("actual supported input admission failed", first, err)
	}
	id := first.Sources[0].ID
	backdate := func() {
		t.Helper()
		s, err := store.Open(ctx, e.db)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetOutlookLastAttempt(ctx, "Main", time.Now().Add(-10*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	backdate()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, st.ModTime().Add(time.Minute), st.ModTime().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	retry, err := syncer.SyncAttendees(ctx, syncer.AttendeeOptions{DBPath: e.db, SourceID: id, Explicit: true})
	if err != nil || retry.Reused != 1 {
		t.Fatal("actual source-ID-only successful retry failed", retry, err)
	}
	s, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.AttendeeSources(ctx, id)
	if err != nil || view.Coverage.Sources[0].Request.SourceID != id || view.Coverage.Sources[0].Request.Root != "" {
		t.Fatal("latest request is not actually exclusive source-ID", view, err)
	}
	read, err := s.OutlookReadReceipt(ctx, "outlook/Main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitOutlook(ctx, store.OutlookBatch{Account: "outlook/Main", At: read.At.Add(time.Minute), Zone: time.UTC,
		Stamp: read.MapperStamp}, func(store.CalendarResult) store.Run {
		return store.Run{StartedAt: read.At, FinishedAt: read.At, Source: "outlook|Main", Status: "ok", Fingerprint: "synthetic-unqualified"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr := e.run("calendar", "attendees", "sync", "--source-id", strconv.FormatInt(id, 10))
	if code == 0 {
		t.Fatal("actual unknown source-ID became successful standalone capture")
	}
	after, err := os.ReadFile(e.db)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("actual CLI refusal changed archive", err)
	}
	fix := errorOf(t, stderr)["fix"].(string)
	parts := strings.Split(fix, "`")
	if len(parts) < 5 || !strings.Contains(parts[1], "sync --outlook-root") {
		t.Fatal("real runner-to-CLI source-ID handoff lost saved ordinary-bootstrap command", fix)
	}
	ordinary, original := attendeeCommandWords(t, parts[1]), attendeeCommandWords(t, parts[3])
	for flag, want := range map[string]string{"--db": e.db, "--max-age": "0", "--teams-root": e.root, "--outlook-root": root} {
		found := false
		for i := 0; i+1 < len(ordinary); i++ {
			found = found || ordinary[i] == flag && ordinary[i+1] == want
		}
		if !found {
			t.Fatal("emitted real recovery escaped selected roots/archive", flag, ordinary)
		}
	}
	if !strings.Contains(parts[3], "--source-id "+strconv.FormatInt(id, 10)) || strings.Contains(parts[3], "--root") {
		t.Fatal("original retry selectors were combined or rewritten", original)
	}
	for _, words := range [][]string{ordinary, original} {
		backdate()
		var out, errout bytes.Buffer
		if code := Main(append(words[1:], "--json"), &out, &errout); code != 0 {
			t.Fatal("actual emitted synthetic recovery command failed", words, code, out.String(), errout.String())
		}
	}
	s, err = store.OpenReadOnly(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	view, err = s.AttendeeSources(ctx, id)
	if err != nil || view.Coverage.Sources[0].Request.Root != "" || view.Coverage.Sources[0].Request.SourceID != id {
		t.Fatal("executed recovery changed the immutable retry request selector", view, err)
	}
}
