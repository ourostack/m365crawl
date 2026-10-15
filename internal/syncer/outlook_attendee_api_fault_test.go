package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestAttendeeCaptureDatabaseFaultBoundaries(t *testing.T) {
	for _, fault := range []string{"closed", "state", "knowledge", "checked", "attempt", "begin", "fingerprint", "origin"} {
		t.Run(fault, func(t *testing.T) {
			isolateTmp(t)
			utcDays(t)
			root, db := attendeeTestRoot(t, false), newDB(t)
			ctx := context.Background()
			s, err := store.Open(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			targets, err := attendeeTargets(ctx, s, AttendeeOptions{Root: root, Explicit: true, Discover: true})
			if err != nil {
				t.Fatal(err)
			}
			oldResolve := peopleResolve
			t.Cleanup(func() { peopleResolve = oldResolve })
			switch fault {
			case "closed":
				_ = s.Close()
			case "state":
				peopleSQL(t, db, `insert into meta(key,value) values('outlook_failure:outlook/Main','{')`)
			case "knowledge":
				peopleSQL(t, db, `alter table knowledge_locators rename column kind to bad_kind`)
			case "checked":
				peopleSQL(t, db, `create trigger fail_checked before insert on meta when new.key like 'outlook_checked:%' begin select raise(abort,'synthetic'); end`)
			case "attempt":
				peopleSQL(t, db, `create trigger fail_attempt before insert on meta when new.key like 'outlook_last_attempt:%' begin select raise(abort,'synthetic'); end`)
			case "begin":
				peopleSQL(t, db, `create trigger fail_begin before insert on knowledge_attempts begin select raise(abort,'synthetic'); end`)
			case "fingerprint":
				peopleResolve = func(path string) (string, error) {
					resolved, err := oldResolve(path)
					if err == nil {
						err = os.Rename(path, path+".retained")
					}
					return resolved, err
				}
			case "origin":
				peopleResolve = func(string) (string, error) { return "", errors.New("synthetic") }
			}
			if status, err := captureAttendee(ctx, s, targets[0]); err == nil || status != "failed" {
				t.Fatal("fault boundary swallowed", fault, status, err)
			}
		})
	}
}

func TestAttendeeAPIOpenAndDiscoveryBoundaries(t *testing.T) {
	for _, fault := range []string{"read", "lock", "open", "default", "abs", "discover", "empty", "skipped", "profile_origin"} {
		t.Run(fault, func(t *testing.T) {
			isolateTmp(t)
			root, db := attendeeTestRoot(t, false), newDB(t)
			open, read, acquire, def, abs, discover, resolve := peopleOpen, peopleOpenRead, peopleAcquire, peopleDefault, peopleAbs, peopleDiscover, peopleResolve
			t.Cleanup(func() {
				peopleOpen, peopleOpenRead, peopleAcquire, peopleDefault, peopleAbs, peopleDiscover, peopleResolve = open, read, acquire, def, abs, discover, resolve
			})
			fail := errors.New("synthetic boundary")
			opts := AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}
			switch fault {
			case "read":
				opts = AttendeeOptions{DBPath: db}
				peopleOpenRead = func(context.Context, string) (*store.Store, error) { return nil, fail }
			case "lock":
				peopleAcquire = func(string) (func(), error) { return nil, fail }
			case "open":
				peopleOpen = func(context.Context, string) (*store.Store, error) { return nil, fail }
			case "default":
				opts.Root = ""
				peopleDefault = func() (string, error) { return "", fail }
			case "abs":
				peopleAbs = func(string) (string, error) { return "", fail }
			case "discover":
				peopleDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
					return nil, nil, nil, fail
				}
			case "empty":
				peopleDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
					return nil, nil, nil, nil
				}
			case "skipped":
				peopleDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
					return nil, nil, []outlookdesktop.SkippedProfile{{Name: "Synthetic"}}, nil
				}
			case "profile_origin":
				peopleResolve = func(path string) (string, error) {
					if filepath.Base(path) == "HxStore.hxd" {
						return "", fail
					}
					return resolve(path)
				}
			}
			if _, err := SyncAttendees(context.Background(), opts); err == nil {
				t.Fatal("IO boundary swallowed", fault)
			}
		})
	}
}
