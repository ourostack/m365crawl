package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

func TestCloudCorrectionPublicChangedIncarnation(t *testing.T) {
	for _, change := range []string{"account", "file"} {
		t.Run(change, func(t *testing.T) {
			e := cloudCLIEnv(t)
			closer := installCloudFixture(t, nil)
			raw := cloudStreamRaw(t, "transcript_observations")
			openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
				return &cloudFixturePage{host: "fixture.sharepoint.com", raw: raw}, closer, nil
			}
			target := "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?id=%2Fsites%2Fsample%2FVideos%2FVideo.mp4"
			if code, _, stderr := e.run("content", "fetch", "stream", target); code != 0 {
				t.Fatal(stderr)
			}
			before := items(t, decode(t, runOK(t, e, "content", "show", "cloud-observation:1")))[0]
			var next map[string]any
			if err := json.Unmarshal([]byte(cloudStreamRaw(t, "no_transcript")), &next); err != nil {
				t.Fatal(err)
			}
			if change == "account" {
				account := next["account"].(map[string]any)
				account["ID"], account["LoginName"] = 12, "other synthetic account"
			} else {
				next["file"].(map[string]any)["FileID"] = "dddddddd-dddd-dddd-dddd-dddddddddddd"
			}
			encoded, err := json.Marshal(next)
			if err != nil {
				t.Fatal(err)
			}
			raw = string(encoded)
			if code, _, stderr := e.run("content", "fetch", "stream", target); code != 0 {
				t.Fatal(stderr)
			}
			for _, args := range [][]string{
				{"content", "list"}, {"content", "search", "streamword"},
				{"content", "show", "cloud-observation:1"}, {"search", "streamword", "--source", "content"},
			} {
				doc := decode(t, runOK(t, e, append([]string{"--max-age", "0"}, args...)...))
				found := false
				for _, item := range items(t, doc) {
					if item["id"] != "cloud-observation:1" {
						continue
					}
					found = true
					oldNative, _ := json.Marshal(before["native_file"])
					actualNative, _ := json.Marshal(item["native_file"])
					oldAccount, _ := json.Marshal(before["native_account"])
					actualAccount, _ := json.Marshal(item["native_account"])
					if item["retained"] != true || item["captured_at"] != before["captured_at"] ||
						string(oldNative) != string(actualNative) || string(oldAccount) != string(actualAccount) ||
						item["selected_transcript"].(map[string]any)["native_id_prefix"] != "T" {
						t.Fatalf("%v presents old incarnation as current or splices identity", args)
					}
				}
				if !found {
					t.Fatalf("%v lost retained body", args)
				}
			}
			doc := decode(t, runOK(t, e, "content", "sources"))
			sources := items(t, doc)
			if len(sources) != 2 || sources[0]["state"] != "retained" ||
				sources[1]["last_good_observation_id"] != nil ||
				sources[1]["current_selection"].(map[string]any)["native_id_prefix"] != nil {
				t.Fatal("coverage/new incarnation borrowed historical body")
			}
		})
	}
}

func TestCloudCorrectionPublicCorruptRetainedBody(t *testing.T) {
	e := cloudCLIEnv(t)
	e.sync()
	closer := installCloudFixture(t, nil)
	raw := cloudStreamRaw(t, "transcript_observations")
	openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		return &cloudFixturePage{host: "fixture.sharepoint.com", raw: raw}, closer, nil
	}
	target := "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?id=%2Fsites%2Fsample%2FVideos%2FVideo.mp4"
	if code, _, stderr := e.run("content", "fetch", "stream", target); code != 0 {
		t.Fatal(stderr)
	}
	raw = cloudStreamRaw(t, "no_transcript")
	if code, _, stderr := e.run("content", "fetch", "stream", target); code != 0 {
		t.Fatal(stderr)
	}
	e.exec("update cloud_observations set result_json=json_set(result_json,'$.file.Title','mutated synthetic title') where rowid=1")
	installCloudFixture(t, func() { t.Fatal("corrupt offline read captured") })
	for _, args := range [][]string{
		{"content", "sources"}, {"content", "search", "absent"},
		{"content", "show", "cloud-observation:1"}, {"content", "list"},
		{"search", "absent", "--source", "content"}, {"search", "absent"},
	} {
		if code, _, _ := e.run(append([]string{"--max-age", "0"}, args...)...); code == 0 {
			t.Fatalf("%v accepted corrupt retained body", args)
		}
	}
}

func TestCloudCorrectionSigninEscapedSiteIdentity(t *testing.T) {
	for _, kind := range []string{"page", "stream"} {
		for _, site := range []string{"sample#suffix", "sample?suffix", "sample%20suffix", "sample space"} {
			t.Run(kind+"/"+site, func(t *testing.T) {
				e := cloudCLIEnv(t)
				page := newTrPage()
				page.host = "fixture.sharepoint.com"
				_, options := fakeBrowser(t, page)
				signinTTY(t, false, "")
				root := "/sites/" + site
				target := url.URL{Scheme: "https", Host: "fixture.sharepoint.com", Path: root + "/SitePages/Page.aspx"}
				if kind == "stream" {
					target.Path = root + "/_layouts/15/stream.aspx"
					target.RawQuery = url.Values{"id": {root + "/Videos/Video.mp4"}}.Encode()
				}
				if code, _, stderr := e.run("content", "signin", target.String(), "--user-agreed"); code != 0 {
					t.Fatal(stderr)
				}
				start, err := url.Parse(options.StartURL)
				if err != nil {
					t.Fatal(err)
				}
				if start.Path != root+"/" || start.RawQuery != "" || start.Fragment != "" ||
					options.Profile != browser.ProfileDir(e.db) || options.DisposableFixture {
					t.Fatalf("admitted site changed or auth defaults changed: %q", options.StartURL)
				}
				if _, err := os.Stat(e.db); !os.IsNotExist(err) {
					t.Fatal("sign-in opened archive")
				}
				rt := &runtime{ctx: context.Background(), dbPath: e.db, stdout: io.Discard, stderr: io.Discard}
				result, err := rt.signinWindow("", browser.KindEdge, "fixture.sharepoint.com", root)
				if err != nil || result.Next != "m365crawl transcripts fetch <meeting>" {
					t.Fatalf("shared transcript sign-in contract changed: %v", err)
				}
				start, err = url.Parse(options.StartURL)
				if err != nil || start.Path != root+"/" || start.RawQuery != "" || start.Fragment != "" || options.DisposableFixture {
					t.Fatal("shared transcript sign-in changed site/auth defaults")
				}
			})
		}
	}
}

func TestCloudCorrectionContentSigninHelpOwnRoute(t *testing.T) {
	e := cloudCLIEnv(t)
	help := strings.Join(strings.Fields(runOK(t, e, "content", "signin", "--help")), " ")
	for _, required := range []string{"m365crawl content signin URL --user-agreed", "Ask the user", "never your everyday browser profile", "do not run it again"} {
		if !strings.Contains(help, required) {
			t.Fatalf("cold content sign-in help lacks %q", required)
		}
	}
	if strings.Contains(help, "m365crawl transcripts signin") {
		t.Fatal("cold content sign-in help uses unrelated transcript-host discovery")
	}
}

func TestCloudCorrectionExplicitNetworkDocumentation(t *testing.T) {
	for _, doc := range []struct {
		path, prefix string
		required     []string
	}{
		{"SPEC.md", "It never talks to the Teams service", []string{"transcripts fetch", "transcripts signin", "content fetch", "content signin", "archive-only"}},
		{"SPEC.md", "- **No credentials", []string{"content fetch", "content signin"}},
		{"SPEC.md", "- **Network only inside a browser", []string{"content fetch", "content signin", "ordinary content reads"}},
		{"SPEC.md", "- **The browser profile", []string{"content fetch", "content signin"}},
		{"SPEC.md", "- **The browser's lifetime", []string{"transcripts signin", "content signin"}},
		{"docs/privacy.md", "m365crawl reads the Teams", []string{"transcripts fetch", "transcripts signin", "content fetch", "content signin", "archive-only"}},
		{"docs/privacy.md", "- **It uses the network only", []string{"transcripts fetch", "transcripts signin", "content fetch", "content signin"}},
		{"README.md", "The archive holds your real chats", []string{"content fetch", "content signin", "archive-only"}},
		{"SPEC.md", "- Commands:", []string{"content fetch page", "content fetch stream", "content signin", "content list", "content search", "content show", "content sources"}},
		{"docs/commands.md", "Every command accepts these", []string{"content list", "content search", "content show", "content sources"}},
	} {
		t.Run(doc.path+"/"+doc.prefix, func(t *testing.T) {
			path := filepath.Join("..", "..", doc.path)
			data, err := os.ReadFile(path) //nolint:gosec // G304: exact repository documentation, not an archive
			if err != nil {
				t.Fatal(err)
			}
			line := ""
			for _, candidate := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(candidate, doc.prefix) {
					line = candidate
					break
				}
			}
			for _, required := range doc.required {
				if !strings.Contains(strings.ToLower(line), strings.ToLower(required)) {
					t.Fatalf("network/privacy/command contract lacks %q: %s", required, line)
				}
			}
		})
	}
}

func TestCloudCorrectionCrossSourceSummaries(t *testing.T) {
	e := cloudCLIEnv(t)
	for _, out := range []string{runOK(t, e, "--help"), runOK(t, e, "--format", "text")} {
		line := ""
		for _, candidate := range strings.Split(out, "\n") {
			if strings.Contains(candidate, `m365crawl search "words"`) {
				line = candidate
			}
		}
		if !strings.Contains(line, "archived cloud content") {
			t.Fatalf("root search summary omits archived cloud content: %s", line)
		}
	}
	job := ""
	for _, line := range strings.Split(skillText(t), "\n") {
		if strings.HasPrefix(line, "| Cross-source and meeting prep |") {
			job = line
		}
	}
	if !strings.Contains(job, "archived cloud content") {
		t.Fatal("guide cross-source job omits archived cloud content")
	}
}
