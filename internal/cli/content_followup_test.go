package cli

import (
	"context"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

func contentFollowupEnv(t *testing.T) *env {
	t.Helper()
	e := cloudCLIEnv(t)
	name := "archive's $HOME & (1).db"
	if goruntime.GOOS == "windows" {
		name = "archive & (1).db"
	}
	e.db = filepath.Join(filepath.Dir(e.db), name)
	return e
}

func contentFollowupWords(t *testing.T, command string) []string {
	t.Helper()
	if goruntime.GOOS == "windows" {
		return posixWords(command) // Native quoted paths contain no double quote.
	}
	shell := exec.Command("sh")
	shell.Stdin = strings.NewReader("set -- " + command + `; printf '%s\000' "$@"`)
	out, err := shell.Output()
	if err != nil {
		t.Fatalf("follow-up shell words: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}

func requireContentFollowupArchive(t *testing.T, e *env, command string) []string {
	t.Helper()
	words := contentFollowupWords(t, command)
	i := slices.Index(words, "--db")
	if len(words) == 0 || words[0] != "m365crawl" || i < 0 || i+1 >= len(words) || words[i+1] != e.db {
		t.Fatalf("follow-up lost exact selected archive: %s", command)
	}
	return words
}

func contentOutputRetries(t *testing.T, e *env, doc map[string]any) []string {
	t.Helper()
	var retries []string
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if key == "retry" {
					retry := child.(string)
					requireContentFollowupArchive(t, e, retry)
					retries = append(retries, retry)
				} else {
					visit(child)
				}
			}
		case []any:
			for _, child := range value {
				visit(child)
			}
		}
	}
	visit(doc)
	return retries
}

func replayContentFollowup(t *testing.T, e *env, command string) (int, string, string) {
	t.Helper()
	words := requireContentFollowupArchive(t, e, command)
	var out, stderr strings.Builder
	code := Main(append(words[1:], "--json", "--teams-root", e.root), &out, &stderr)
	return code, out.String(), stderr.String()
}

func contentFixCommands(fix string) []string {
	var commands []string
	for i, part := range strings.Split(fix, "`") {
		if i%2 == 1 && strings.HasPrefix(part, "m365crawl ") {
			commands = append(commands, part)
		}
	}
	return commands
}

func TestContentLocalFollowupsKeepArchiveAndSelection(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("native synthetic file capture")
	}
	for _, from := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "reference"}[from], func(t *testing.T) {
			e := contentFollowupEnv(t)
			original := localCLIFile(t, e)
			name := "source's $HOME & (1).docx"
			if goruntime.GOOS == "windows" {
				name = "source & (1).docx"
			}
			path := filepath.Join(filepath.Dir(original), name)
			if err := os.Rename(original, path); err != nil {
				t.Fatal(err)
			}
			selection := []string{"--file", path}
			if from {
				native := filepath.ToSlash(path)
				if !strings.HasPrefix(native, "/") {
					native = "/" + native
				}
				seedLocalReferenceCLI(t, e, (&url.URL{Scheme: "file", Path: native}).String())
				selection = []string{"--from", "document-observation:1"}
			}
			code, out, stderr := e.run(append([]string{"content", "capture"}, selection...)...)
			if code != 0 {
				t.Fatal(stderr)
			}
			if len(contentOutputRetries(t, e, decode(t, out))) == 0 {
				t.Fatal("capture coverage lost")
			}
			if code, _, stderr := e.run("content", "capture", "--file", path+".missing", "--kind", "docx"); code == 0 {
				t.Fatal("missing synthetic input accepted")
			} else {
				commands := contentFixCommands(errorOf(t, stderr)["fix"].(string))
				if len(commands) == 0 {
					t.Fatal("no executable failure follow-up")
				}
				for _, command := range commands {
					requireContentFollowupArchive(t, e, command)
				}
			}
			for _, args := range [][]string{
				{"content", "sources"}, {"content", "list"}, {"content", "search", "absentword"},
				{"content", "show", "local-text-observation:1"},
				{"search", "absentword", "--source", "content", "--fields", "id", "--limit", "1"},
			} {
				code, out, stderr := e.run(args...)
				if code != 0 {
					t.Fatal(stderr)
				}
				retries := contentOutputRetries(t, e, decode(t, out))
				if len(retries) == 0 {
					t.Fatalf("missing coverage: %v", args)
				}
				for _, retry := range retries {
					words := requireContentFollowupArchive(t, e, retry)
					kind := slices.Index(words, "--kind")
					if kind < 0 || words[kind+1] != "docx" || slices.Contains(words, "--source-id") {
						t.Fatal("registered requested kind changed or source ID used for retry")
					}
				}
			}
			code, out, stderr = e.run("content", "sources")
			if code != 0 {
				t.Fatal(stderr)
			}
			for _, item := range items(t, decode(t, out)) {
				if code, _, _ := replayContentFollowup(t, e, item["retry"].(string)); (code == 0) != (item["state"] != "failed") {
					t.Fatal("standalone local retry changed target/archive semantics")
				}
			}
		})
	}
}

func TestContentCloudFollowupsKeepArchiveAndRegistration(t *testing.T) {
	e := contentFollowupEnv(t)
	closer := installCloudFixture(t, nil)
	pageURL := "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"
	streamURL := "https://fixture.sharepoint.com/sites/sample/_layouts/15/stream.aspx?id=%2Fsites%2Fsample%2FVideos%2FVideo.mp4"
	if code, _, stderr := e.run("content", "fetch", "page", pageURL); code != 0 {
		t.Fatal(stderr)
	}
	raw := cloudStreamRaw(t, "transcript_observations")
	openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		return &cloudFixturePage{host: "fixture.sharepoint.com", raw: raw}, closer, nil
	}
	if code, _, stderr := e.run("content", "fetch", "stream", streamURL, "--transcript-id", "T"); code != 0 {
		t.Fatal(stderr)
	}
	openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		return &cloudFixturePage{host: "login.microsoftonline.com"}, closer, nil
	}
	if code, _, stderr := e.run("content", "fetch", "stream", streamURL, "--transcript-id", "other"); code == 0 {
		t.Fatal("signin refusal absent")
	} else {
		commands := contentFixCommands(errorOf(t, stderr)["fix"].(string))
		if len(commands) < 2 {
			t.Fatal("signin failure must give both scoped sign-in and exact registration retry")
		}
		for _, command := range commands {
			requireContentFollowupArchive(t, e, command)
		}
	}
	for _, args := range [][]string{
		{"content", "sources"}, {"content", "list"}, {"content", "search", "absentword"},
		{"content", "show", "cloud-observation:1"}, {"search", "absentword", "--source", "content"},
	} {
		code, out, stderr := e.run(args...)
		if code != 0 {
			t.Fatal(stderr)
		}
		if len(contentOutputRetries(t, e, decode(t, out))) == 0 {
			t.Fatal("cloud coverage lost")
		}
	}
	code, out, stderr := e.run("content", "sources")
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, item := range items(t, decode(t, out)) {
		retry := item["retry"].(string)
		words := requireContentFollowupArchive(t, e, retry)
		if !slices.Contains(words, "--registration-id") || slices.Contains(words, "--source-id") || slices.Contains(words, "--transcript-id") {
			t.Fatal("retry reconstructed or changed stored selection")
		}
		// The original selected request is recovered from this archive before any driver use.
		if code, _, stderr := replayContentFollowup(t, e, retry); code == 0 || !strings.Contains(stderr, "content_signin_required") {
			t.Fatal("standalone registration retry lost request/archive")
		}
	}
}

func TestContentWrongKindRecoveryChangesSelectionWithoutChangingEvidence(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("native synthetic file capture")
	}
	for _, from := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "reference"}[from], func(t *testing.T) {
			e := contentFollowupEnv(t)
			path := localCLIFile(t, e)
			selection := []string{"--file", path}
			if from {
				native := filepath.ToSlash(path)
				if !strings.HasPrefix(native, "/") {
					native = "/" + native
				}
				seedLocalReferenceCLI(t, e, (&url.URL{Scheme: "file", Path: native}).String())
				selection = []string{"--from", "document-observation:1"}
			}
			args := append(append([]string{"content", "capture"}, selection...), "--kind", "xlsx")
			code, _, stderr := e.run(args...)
			if code == 0 || errorOf(t, stderr)["code"] != "local_text_unsupported" {
				t.Fatal("mismatch changed failure classification")
			}
			fix := errorOf(t, stderr)["fix"].(string)
			if !strings.Contains(strings.ToLower(fix), "format") || !strings.Contains(fix, "extension") {
				t.Fatal("unsupported recovery lacks format/extension qualification")
			}
			recovered := false
			for _, command := range contentFixCommands(fix) {
				words := requireContentFollowupArchive(t, e, command)
				if !slices.Contains(words, "capture") {
					continue
				}
				if slices.Contains(words, "--kind") || (from && !slices.Contains(words, "--from")) {
					t.Fatal("recovery repeats rejected intent or drops selected reference")
				}
				code, out, stderr := replayContentFollowup(t, e, command)
				if code != 0 {
					t.Fatalf("changed explicit recovery failed: %s", stderr)
				}
				if items(t, decode(t, out))[0]["verified_kind"] != "docx" {
					t.Fatal("changed selection was not package-verified docx")
				}
				if from {
					code, detail, stderr := e.run("content", "show", "local-text-observation:1")
					if code != 0 {
						t.Fatal(stderr)
					}
					refs := items(t, decode(t, detail))[0]["references"].([]any)
					if len(refs) != 1 || refs[0].(map[string]any)["Reference"].(map[string]any)["DocumentObservationID"] != float64(1) {
						t.Fatal("reference recovery lost original selected origin")
					}
				}
				recovered = true
			}
			if !recovered {
				t.Fatal("no actionable changed selection")
			}
			st, err := store.OpenReadOnly(context.Background(), e.db)
			if err != nil {
				t.Fatal(err)
			}
			sources, err := st.LocalTextSources(context.Background(), 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			if len(sources) != 2 || sources[0].RequestedKind != "xlsx" || sources[0].Source.Reason != "local_text_unsupported" || sources[1].RequestedKind != "docx" {
				t.Fatal("recovery silently rewrote failed requested-kind evidence")
			}
		})
	}
}

func TestContentUnsupportedRecoveryAndOtherStatesStayDistinct(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("native synthetic file capture")
	}
	for _, mode := range []string{"unsupported", "renamed", "compound", "too-large"} {
		t.Run(mode, func(t *testing.T) {
			e := contentFollowupEnv(t)
			path := localCLIFile(t, e)
			kind := "docx"
			switch mode {
			case "unsupported":
				if err := os.WriteFile(path, []byte("ordinary unsupported synthetic bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "renamed":
				if err := os.Rename(path, path+".data"); err != nil {
					t.Fatal(err)
				}
				path, kind = path+".data", "xlsx"
			case "compound":
				if err := os.WriteFile(path, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, 0o600); err != nil {
					t.Fatal(err)
				}
			case "too-large":
				root, err := os.OpenRoot(filepath.Dir(path))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = root.Close() }()
				f, err := root.OpenFile(filepath.Base(path), os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate((64 << 20) + 1)
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatal("synthetic oversized fixture")
				}
			}
			code, out, stderr := e.run("content", "capture", "--file", path, "--kind", kind)
			if mode == "compound" {
				if code != 0 {
					t.Fatal(stderr)
				}
				item := items(t, decode(t, out))[0]
				if item["verified_kind"] != nil || item["state"] != "compound_unreadable" {
					t.Fatal("compound intent became verified format")
				}
				return
			}
			if code == 0 {
				t.Fatal("fatal input published as success")
			}
			failure := errorOf(t, stderr)
			if mode == "too-large" {
				if failure["code"] != "local_text_too_large" {
					t.Fatal("size refusal became format mismatch")
				}
				return
			}
			if failure["code"] != "local_text_unsupported" || !strings.Contains(failure["fix"].(string), "Inspect the actual format") {
				t.Fatal("unsupported input lacks honest format inspection guidance")
			}
			for _, command := range contentFixCommands(failure["fix"].(string)) {
				if slices.Contains(requireContentFollowupArchive(t, e, command), "capture") {
					t.Fatal("unsupported/unknown extension invented a corrective capture")
				}
			}
		})
	}
}

func TestContentFollowupRecoveryGuidancePortable(t *testing.T) {
	e := contentFollowupEnv(t)
	rt := &runtime{g: &Globals{}, dbPath: e.db}
	for _, tc := range []struct {
		extension, kind, code string
		ref                   bool
		capture               bool
	}{
		{".DOCX", "xlsx", "local_text_unsupported", false, true},
		{".docx", "docx", "local_text_unsupported", false, false},
		{".data", "xlsx", "local_text_unsupported", true, false},
		{".docx", "docx", "local_text_too_large", true, true},
	} {
		input := officecapture.TextInput{Path: filepath.Join(filepath.Dir(e.db), "synthetic"+tc.extension), RequestedKind: tc.kind}
		var ref *store.LocalTextReference
		if tc.ref {
			ref = &store.LocalTextReference{DocumentObservationID: 7}
		}
		fix := rt.localCaptureFix(input, ref, tc.code)
		capture := false
		for _, command := range contentFixCommands(fix) {
			words := requireContentFollowupArchive(t, e, command)
			capture = capture || slices.Contains(words, "capture")
		}
		if capture != tc.capture {
			t.Fatalf("format recovery classification: %+v %s", tc, fix)
		}
	}
}

func TestContentSigninNextAndUnknownFixKeepArchive(t *testing.T) {
	e := contentFollowupEnv(t)
	for _, id := range []string{"local-text-observation:1", "cloud-observation:1"} {
		if code, _, stderr := e.run("content", "show", id); code == 0 {
			t.Fatal("unknown observation succeeded")
		} else {
			commands := contentFixCommands(errorOf(t, stderr)["fix"].(string))
			if len(commands) != 2 {
				t.Fatal("unknown observation needs scoped sources/list routes")
			}
			for _, command := range commands {
				requireContentFollowupArchive(t, e, command)
			}
		}
	}
	page := newTrPage()
	fakeBrowser(t, page)
	signinTTY(t, false, "")
	target := "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"
	if code, _, stderr := e.run("content", "signin", target); code == 0 {
		t.Fatal("missing consent admitted")
	} else {
		for _, command := range contentFixCommands(errorOf(t, stderr)["fix"].(string)) {
			requireContentFollowupArchive(t, e, command)
		}
	}
	if code, out, stderr := e.run("content", "signin", target, "--user-agreed"); code != 0 {
		t.Fatal(stderr)
	} else {
		words := requireContentFollowupArchive(t, e, decode(t, out)["next"].(string))
		if !slices.Contains(words, target) || !slices.Contains(words, "page") {
			t.Fatal("post-signin command lost actual target kind/URL")
		}
	}
}
