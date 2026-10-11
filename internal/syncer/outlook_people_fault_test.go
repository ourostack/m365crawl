package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookpeople"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestPeopleCleanupMustProveOwnedAbsence(t *testing.T) {
	for _, mode := range []string{"payload", "root", "stat-error", "cancel", "read-error", "capture-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root := t.TempDir()
			profile := filepath.Join(root, "Fixture")
			if err := os.Mkdir(profile, 0o700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(profile, outlookdesktop.StoreFileName)
			if err := os.WriteFile(source, []byte("synthetic"), 0o600); err != nil {
				t.Fatal(err)
			}
			oldSnapshot, oldCollect, oldStat := peopleSnapshot, peopleCollect, peopleStat
			t.Cleanup(func() { peopleSnapshot, peopleCollect, peopleStat = oldSnapshot, oldCollect, oldStat })
			var privateRoot string
			peopleSnapshot = func(context.Context, string) (outlookdesktop.Info, func(), error) {
				if mode == "capture-error" {
					return outlookdesktop.Info{}, func() {}, errors.New("capture")
				}
				privateRoot = t.TempDir()
				path := filepath.Join(privateRoot, outlookdesktop.StoreFileName)
				if err := os.WriteFile(path, []byte("copy"), 0o600); err != nil {
					t.Fatal(err)
				}
				return outlookdesktop.Info{Path: path, Size: 4, ModTime: time.Now().UTC()}, func() {
					if mode != "payload" {
						if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
							t.Fatal(err)
						}
					}
					if mode != "payload" && mode != "root" {
						if err := os.Remove(privateRoot); err != nil && !os.IsNotExist(err) {
							t.Fatal(err)
						}
					}
					if mode == "cancel" {
						cancel()
					}
				}, nil
			}
			peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
				if mode == "read-error" {
					return outlookpeople.Result{}, "", errors.New("read")
				}
				return outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: "fixture"}}}, "copy", nil
			}
			if mode == "stat-error" {
				peopleStat = func(string) (os.FileInfo, error) { return nil, errors.New("stat") }
			}
			db := newDB(t)
			report, err := SyncPeople(ctx, PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
			if mode == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled capture: %v", err)
				}
			} else if err != nil || report.Failed != 1 {
				t.Fatalf("capture failure: %+v %v", report, err)
			}
			s, err := store.OpenReadOnly(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			rows, _, err := s.PeopleObservations(context.Background(), "", 0, 10)
			if err != nil || len(rows) != 0 {
				t.Fatal("uncertain acquisition admitted observations")
			}
			sources, err := s.KnowledgeSources(context.Background(), "outlook_people")
			if err != nil || len(sources) != 1 || sources[0].Status == "pending" {
				t.Fatalf("terminal failure missing: %+v %v", sources, err)
			}
		})
	}
}
