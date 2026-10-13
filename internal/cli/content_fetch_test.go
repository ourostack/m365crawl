package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/sharepointcontent"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

func cloudStreamRaw(t *testing.T, state string) string {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(cloudFixtureRaw), &raw); err != nil {
		t.Fatal(err)
	}
	raw["state"] = state
	delete(raw, "pageControls")
	file := raw["file"].(map[string]any)
	file["Path"] = "/sites/sample/Videos/Video.mp4"
	file["DriveID"] = "drive"
	file["ItemID"] = "item"
	if state == "transcript_observations" {
		raw["transcript"] = map[string]any{"ID": "T", "Entries": []any{map[string]any{"Ordinal": 0, "ID": "E", "Text": "streamword", "StartRaw": "00:00:01", "EndRaw": "00:00:02"}}}
	}
	if state == "metadata_only" {
		file["ModifiedRaw"] = "unknown"
	}
	encoded, _ := json.Marshal(raw)
	return string(encoded)
}

func TestContentStreamRetainedExplicitFetch(t *testing.T) {
	e := cloudCLIEnv(t)
	closer := installCloudFixture(t, nil)
	if code, _, stderr := e.run("content", "fetch", "page", "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"); code != 0 {
		t.Fatal(stderr)
	}
	for _, state := range []string{"transcript_observations", "metadata_only", "no_transcript"} {
		raw := cloudStreamRaw(t, state)
		openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
			return &cloudFixturePage{host: "fixture.sharepoint.com", raw: raw}, closer, nil
		}
		args := []string{"content", "fetch", "stream", "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?id=%2Fsites%2Fsample%2FVideos%2FVideo.mp4", "--transcript-id", "T"}
		code, out, stderr := e.run(args...)
		if code != 0 {
			t.Fatal(stderr)
		}
		doc := decode(t, out)
		if state != "transcript_observations" && doc["retained_observation_id"] == nil {
			t.Fatal("retained body missing")
		}
		if state == "metadata_only" && len(doc["losses"].(map[string]any)) != 1 {
			t.Fatal("loss count hidden")
		}
	}
	for _, format := range []string{"json", "text"} {
		if code, _, stderr := e.run("--format", format, "content", "sources"); code != 0 {
			t.Fatal(stderr)
		}
		if code, _, stderr := e.run("--format", format, "content", "search", "streamword"); code != 0 {
			t.Fatal(stderr)
		}
	}
}

func TestContentFetchErrorsAndPublicationLock(t *testing.T) {
	for _, mode := range []string{"signin", "read-error", "cancel", "record-fault", "begin-fault", "commit-fault", "snapshot-fault", "busy"} {
		t.Run(mode, func(t *testing.T) {
			e := cloudCLIEnv(t)
			oldOpen := openArchive
			t.Cleanup(func() { openArchive = oldOpen })
			closer := installCloudFixture(t, nil)
			var release func()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raw := cloudFixtureRaw
			host := "fixture.sharepoint.com"
			if mode == "signin" {
				host = "login.microsoftonline.com"
			}
			if mode == "read-error" {
				raw = `{"state":"failed"}`
			}
			if mode == "begin-fault" {
				openArchive = func(context.Context, string) (*store.Store, error) { return nil, errors.New("synthetic open") }
			}
			openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
				if mode == "busy" {
					var err error
					release, err = store.AcquireLock(e.db)
					if err != nil {
						t.Fatal(err)
					}
				}
				if mode == "record-fault" {
					openArchive = func(context.Context, string) (*store.Store, error) {
						return nil, errors.New("synthetic diagnostic write")
					}
					closer.err = errors.New("close")
				}
				if mode == "commit-fault" || mode == "snapshot-fault" {
					db, err := sql.Open("sqlite", e.db)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = db.Close() }()
					statement := "create trigger cloud_fault before insert on cloud_observations begin select raise(abort,'synthetic'); end"
					if mode == "snapshot-fault" {
						statement = "create trigger cloud_fault after insert on cloud_capture_metadata begin update cloud_locator_metadata set request_json='{}'; end"
					}
					if _, err := db.Exec(statement); err != nil {
						t.Fatal(err)
					}
				}
				page := &cloudFixturePage{raw: raw, host: host}
				if mode == "cancel" {
					page.cancel = cancel
				}
				return page, closer, nil
			}
			req, _ := sharepointcontent.NormalizeRequest(sharepointcontent.Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"})
			rt := &runtime{ctx: ctx, g: &Globals{}, dbPath: e.db, stdout: io.Discard, stderr: io.Discard}
			_, err := rt.fetchCloud(req, browser.LaunchOptions{Profile: browser.ProfileDir(e.db), Headless: true})
			if release != nil {
				release()
			}
			if err == nil {
				t.Fatal("terminal error success-shaped")
			}
			if mode != "begin-fault" && closer.n != 1 {
				t.Fatal("browser still live at failed publication")
			}
			if mode == "signin" && !strings.Contains(err.Error(), "not signed in") {
				t.Fatal("wrong explicit signin fix")
			}
		})
	}
	e := cloudCLIEnv(t)
	if code, _, _ := e.run("content", "fetch", "page", "--registration-id", "1"); code == 0 {
		t.Fatal("missing retry archive")
	}
	if code, _, _ := e.run("--account", "t/u", "content", "fetch", "page", "http://invalid"); code == 0 {
		t.Fatal("fetch flags")
	}
	if code, _, _ := e.run("content", "fetch", "page", "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx", "--browser", "/missing-browser"); code == 0 {
		t.Fatal("missing browser")
	}
	rt := &runtime{ctx: context.Background(), g: &Globals{}, dbPath: e.db, stdout: io.Discard}
	if _, err := rt.fetchCloud(sharepointcontent.CanonicalRequest{}, browser.LaunchOptions{}); err == nil {
		t.Fatal("invalid canonical")
	}
}

func cloudCLIEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	return e
}
func installCloudFixture(t *testing.T, fn func()) *cloudFixtureCloser {
	t.Helper()
	old := openBrowser
	t.Cleanup(func() { openBrowser = old })
	closer := &cloudFixtureCloser{}
	openBrowser = func(_ context.Context, o browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		if !o.Headless || o.Profile == "" || o.DisposableFixture {
			t.Fatal("capture not private/headless")
		}
		if fn != nil {
			fn()
		}
		return &cloudFixturePage{raw: cloudFixtureRaw, host: "fixture.sharepoint.com"}, closer, nil
	}
	return closer
}
func TestContentFetchPublicWorkflow(t *testing.T) {
	e := cloudCLIEnv(t)
	closer := installCloudFixture(t, nil)
	code, out, stderr := e.run("content", "fetch", "page", "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx?ticket=private")
	if code != 0 {
		t.Fatalf("fetch: %s", stderr)
	}
	report := decode(t, out)
	if report["state"] != "page_observations" || report["partial"] != true || closer.n != 1 {
		t.Fatal("scope or cleanup")
	}
	reg := strconv.FormatInt(int64(report["registration_id"].(float64)), 10)
	if code, _, stderr = e.run("content", "fetch", "page", "--registration-id", reg); code != 0 {
		t.Fatal(stderr)
	}
	if closer.n != 2 {
		t.Fatal("explicit retry did not capture")
	}
	for _, args := range [][]string{
		{"content", "fetch", "page", "https://other.invalid/P.aspx"},
		{"content", "fetch", "stream", "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?id=%2Fsites%2Fsample%2FVideo.mp4", "--transcript-id="},
		{"content", "fetch", "page", "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx", "--registration-id", reg},
		{"content", "fetch", "stream", "--registration-id", reg},
		{"content", "fetch", "page", "--registration-id", "999"},
	} {
		if code, _, _ := e.run(args...); code == 0 {
			t.Fatalf("invalid accepted: %v", args)
		}
	}
	if closer.n != 2 {
		t.Fatal("invalid request launched")
	}
}
func TestContentFetchInvalidBeforeArchive(t *testing.T) {
	e := cloudCLIEnv(t)
	closer := installCloudFixture(t, nil)
	if code, _, _ := e.run("content", "fetch", "page", "http://invalid"); code == 0 {
		t.Fatal("invalid input")
	}
	if _, err := os.Stat(e.db); !os.IsNotExist(err) {
		t.Fatal("invalid input opened archive")
	}
	if closer.n != 0 {
		t.Fatal("invalid launched")
	}
}
func TestContentFetchPublicationFaultDiscards(t *testing.T) {
	e := cloudCLIEnv(t)
	closer := installCloudFixture(t, func() {
		release, err := store.AcquireLock(e.db)
		if err != nil {
			t.Fatal("archive lock held across capture")
		}
		defer release()
		st, err := store.Open(context.Background(), e.db)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st.Close() }()
	})
	closer.err = errors.New("private close")
	code, _, stderr := e.run("content", "fetch", "page", "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx")
	if code == 0 || strings.Contains(stderr, "private close") {
		t.Fatal("close failure masked/leaked")
	}
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	snap, err := st.CloudSnapshot(context.Background(), store.CloudFilter{})
	if err != nil || len(snap.Rows) != 0 || len(snap.Sources) != 1 {
		t.Fatal("failed close published")
	}
}
