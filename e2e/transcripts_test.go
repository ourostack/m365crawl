//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

// downloadToken marks the stub's temporary download URL: it must never come back from the page.
const downloadToken = "synthetic-download-token"

// sharePointStub stands in for the two SharePoint endpoints the in-page script calls: the item's
// metadata, which names the transcripts and their temporary download URLs, and the download.
// Each item id is one case.
func sharePointStub(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	transcript := func(id, kind string) map[string]any {
		return map[string]any{"id": id, "temporaryDownloadUrl": srv.URL + "/download/" + kind + "?token=" + downloadToken}
	}
	media := func(w http.ResponseWriter, ts ...map[string]any) {
		w.Header().Set("Content-Type", "application/json; odata.metadata=minimal")
		_ = json.NewEncoder(w).Encode(map[string]any{"media": map[string]any{"transcripts": ts}})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><title>stub</title>"))
	})
	mux.HandleFunc("/sites/stub/_api/v2.1/drives/D/items/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("$expand") != "media/transcripts" || r.Header.Get("Accept") != "application/json" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		switch filepath.Base(r.URL.Path) {
		case "ok", "big":
			media(w, transcript("T1", filepath.Base(r.URL.Path)))
		case "pick":
			media(w, transcript("T0", "wrong"), transcript("T1", "ok"))
		case "only":
			media(w, transcript("other", "ok"))
		case "ambiguous":
			media(w, transcript("T8", "ok"), transcript("T9", "ok"))
		case "none":
			media(w)
		case "dl403":
			media(w, transcript("T1", "forbidden"))
		case "badjson":
			media(w, transcript("T1", "badjson"))
		case "forbidden":
			http.Error(w, "{}", http.StatusForbidden)
		case "unauth":
			http.Error(w, "{}", http.StatusUnauthorized)
		case "missing":
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("<html>not found</html>"))
		case "login":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><form>sign in</form></html>"))
		default:
			http.Error(w, "{}", http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" || r.URL.Query().Get("token") != downloadToken {
			http.Error(w, "bad download request", http.StatusBadRequest)
			return
		}
		switch filepath.Base(r.URL.Path) {
		case "forbidden":
			http.Error(w, "{}", http.StatusForbidden)
			return
		case "badjson":
			_, _ = w.Write([]byte("{not json"))
			return
		case "wrong":
			_ = json.NewEncoder(w).Encode(map[string]any{"entries": []map[string]any{{"speakerDisplayName": "Wrong Speaker", "text": "Wrong transcript."}}})
			return
		case "big":
			_, _ = w.Write([]byte(`{"entries":[],"pad":"` + strings.Repeat("x", 4096) + `"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"entries": []map[string]any{
			{"speakerDisplayName": "Speaker A", "startOffset": "00:00:01.5000000", "endOffset": "00:00:03.0000000", "text": "Synthetic line one."},
			{"speakerDisplayName": "Speaker B", "startOffset": "00:01:02", "endOffset": "00:01:04", "text": "Synthetic line two."},
		}})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestFetchScriptInRealHeadlessChrome runs the real in-page script in a headless Edge or Chrome
// with a temporary profile, against a local stub of SharePoint. Nothing leaves this machine.
func TestFetchScriptInRealHeadlessChrome(t *testing.T) {
	exe, kind, err := browser.Find("")
	if err != nil {
		if os.Getenv(requireBrowserEnv) == "1" {
			t.Fatalf("no browser found, and %s=1 says this runner must have one: %v", requireBrowserEnv, err)
		}
		t.Skipf("no browser installed: %v", err)
	}
	srv := sharePointStub(t)
	profile := filepath.Join(t.TempDir(), "archive", "browser")
	t.Cleanup(func() { _ = browser.SweepOrphan(profile) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b, err := browser.Launch(ctx, browser.LaunchOptions{Exe: exe, Kind: kind, Profile: profile, Headless: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer func() { _ = b.Close() }()
	page, err := b.Page(ctx)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if err := page.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	base := srv.URL + "/sites/stub/_api/v2.1/drives/D/items/"
	for _, c := range []struct {
		item, base, state string
		status, entries   int
	}{
		{"ok", "", "ok", 200, 2},
		{"pick", "", "ok", 200, 2},
		{"only", "", "ok", 200, 2},
		{"ambiguous", "", "no_transcript", 200, 0},
		{"none", "", "no_transcript", 200, 0},
		{"big", "", "too_large", 200, 0},
		{"dl403", "", "no_access", 403, 0},
		{"badjson", "", "failed", 0, 0},
		{"forbidden", "", "no_access", 403, 0},
		{"unauth", "", "signin", 401, 0},
		{"missing", "", "not_found", 404, 0},
		{"login", "", "signin", 200, 0},
		{"error", "", "failed", 500, 0},
		{"throw", "http://127.0.0.1:1/sites/stub/_api/v2.1/drives/D/items/", "signin_probe", 0, 0},
	} {
		b := base
		if c.base != "" {
			b = c.base
		}
		maxBytes := int64(transcripts.DefaultMaxBytes)
		if c.item == "big" {
			maxBytes = 1024
		}
		var raw json.RawMessage
		expr := transcripts.ScriptExpr(transcripts.ScriptArgs{Base: b + c.item, TranscriptID: "T1", MaxBytes: maxBytes, LoginHosts: transcripts.LoginHosts})
		if err := page.Eval(ctx, expr, &raw); err != nil {
			t.Fatalf("%s: %v", c.item, err)
		}
		if strings.Contains(string(raw), downloadToken) || strings.Contains(string(raw), "/download/") || strings.Contains(string(raw), srv.URL) {
			t.Fatalf("%s: the result carries a URL: %s", c.item, raw)
		}
		var res transcripts.ScriptResult
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatalf("%s: %v: %s", c.item, err, raw)
		}
		if res.State != c.state || res.Status != c.status || len(res.Entries) != c.entries {
			t.Errorf("%s: got %s", c.item, raw)
		}
		if c.item == "badjson" && res.Error != "SyntaxError" {
			t.Errorf("badjson: error %q, want only the exception's class name", res.Error)
		}
		if c.entries > 0 {
			got := fmt.Sprintf("%+v", res.Entries[0])
			if got != "{S:Speaker A B:00:00:01.5000000 E:00:00:03.0000000 T:Synthetic line one.}" {
				t.Errorf("%s: first entry %s", c.item, got)
			}
			if es, bad := transcripts.DecodeEntries(res.Entries); bad != 0 || *es[1].StartMS != 62000 {
				t.Errorf("%s: decoded %+v, %d bad", c.item, es, bad)
			}
		}
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
