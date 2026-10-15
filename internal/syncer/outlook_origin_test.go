package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestAttendeeOrdinaryOriginQualification(t *testing.T) {
	resolve, abs := peopleResolve, peopleAbs
	t.Cleanup(func() { peopleResolve, peopleAbs = resolve, abs })
	peopleResolve = func(string) (string, error) { return "", errors.New("synthetic") }
	if outlookSourceOrigin("synthetic") != "" {
		t.Fatal("failed resolution invented origin")
	}
	peopleResolve = func(string) (string, error) { return "/synthetic/source", nil }
	peopleAbs = func(string) (string, error) { return "", errors.New("synthetic") }
	if outlookSourceOrigin("synthetic") != "" {
		t.Fatal("failed absolute resolution invented origin")
	}
	peopleResolve, peopleAbs = resolve, abs
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	target := filepath.Join(root, "Main", "HxStore.hxd")
	calls := 0
	peopleResolve = func(path string) (string, error) {
		if path == target {
			calls++
			if calls > 1 {
				return "/synthetic/changed/Main/HxStore.hxd", nil
			}
		}
		return resolve(path)
	}
	o := outlookOpts(db, root)
	o.Root = t.TempDir()
	rep, _ := run(t, o)
	if sourceKeyed(t, rep, "outlook|Main").Status != StatusOK {
		t.Fatal("origin qualification undid healthy legacy calendar")
	}
	s, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	receipt, err := s.OutlookReadReceipt(context.Background(), "outlook/Main")
	if err != nil || receipt.ID == 0 || receipt.Origin != "" {
		t.Fatal("changed source origin received positive qualification")
	}
}

func TestAttendeeOrdinaryReceiptFailure(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	ctx := context.Background()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := openRaw(t, db).Exec(`drop trigger outlook_attendee_link_insert`); err != nil {
		t.Fatal(err)
	}
	r := runner{st: s, o: outlookOpts(db, root)}
	if _, _, _, err := r.outlook(ctx, outlookdesktop.Profile{Name: "Main", StorePath: filepath.Join(root, "Main", "HxStore.hxd")}, &Report{}); err == nil {
		t.Fatal("ordinary runner hid receipt namespace failure")
	}
}
