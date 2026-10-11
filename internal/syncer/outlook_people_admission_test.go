package syncer

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookpeople"
	"github.com/ourostack/m365crawl/internal/store"
)

func peopleSQL(t *testing.T, path, statement string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func TestPeopleAdmissionFailuresAreTerminal(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, fail := range []string{"link", "admission", "stale", "diagnostic"} {
			t.Run(fail+map[bool]string{true: "-shared"}[shared], func(t *testing.T) {
				root := mailRoot(t)
				db := newDB(t)
				old := peopleCollect
				t.Cleanup(func() { peopleCollect = old })
				peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
					return outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: "fixture"}}}, "one", nil
				}
				if _, err := SyncPeople(context.Background(), PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil {
					t.Fatal(err)
				}
				peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
					switch fail {
					case "link":
						peopleSQL(t, db, "drop table calendar_account_links")
					case "admission":
						peopleSQL(t, db, "drop table people_observations")
					case "stale":
						peopleSQL(t, db, "update knowledge_locators set latest_attempt=latest_attempt+100")
					case "diagnostic":
						peopleSQL(t, db, "drop table knowledge_attempts")
						return outlookpeople.Result{}, "", errors.New("read")
					}
					return outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 2, Email: "new"}}}, "two", nil
				}
				if shared {
					s, err := store.Open(context.Background(), db)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = s.Close() }()
					source, err := filepath.EvalSymlinks(filepath.Join(root, "Main", outlookdesktop.StoreFileName))
					if err != nil {
						t.Fatal(err)
					}
					p, err := beginSharedPeople(context.Background(), s, source, "signature", time.Now(), true)
					if err != nil {
						t.Fatal(err)
					}
					info, cleanup, err := outlookdesktop.Snapshot(context.Background(), source)
					if err != nil {
						t.Fatal(err)
					}
					out := finishSharedPeople(context.Background(), s, "Main", info, cleanup, nil, *p)
					if out.report.Error == nil {
						t.Fatal("shared admission error hidden")
					}
				} else if _, err := SyncPeople(context.Background(), PeopleOptions{DBPath: db, Explicit: true}); err == nil {
					t.Fatal("standalone admission error hidden")
				}
			})
		}
	}
}

func TestPeopleSharedRegistrationAndBeginFailures(t *testing.T) {
	root := mailRoot(t)
	db := newDB(t)
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	path := filepath.Join(root, "Main", outlookdesktop.StoreFileName)
	if pending, err := beginSharedPeople(context.Background(), s, path, "", time.Now(), true); err != nil || pending != nil {
		t.Fatalf("unregistered shared source: %v", err)
	}
	resetPeopleSeams(t)
	peopleAbs = func(string) (string, error) { return "", errors.New("abs") }
	if _, err := beginSharedPeople(context.Background(), s, path, "", time.Now(), true); err == nil {
		t.Fatal("absolute path failure hidden")
	}
	peopleAbs = filepath.Abs
	source, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginKnowledgeAttempt(context.Background(), store.KnowledgeInput{Kind: "outlook_people", Platform: "synthetic", Locator: source, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	peopleResolve = func(string) (string, error) {
		peopleSQL(t, db, "drop table knowledge_attempts")
		return source, nil
	}
	if _, err := beginSharedPeople(context.Background(), s, path, "", time.Now(), true); err == nil {
		t.Fatal("begin failure hidden")
	}
}
