package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/officeregistry"
	"github.com/ourostack/m365crawl/internal/onedriveusage"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func recoveryArchives(t *testing.T) (a, b *env) {
	t.Helper()
	a = newEnv(t)
	home := t.TempDir()
	for key, value := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"LOCALAPPDATA": filepath.Join(home, "AppData", "Local"),
		"APPDATA":      filepath.Join(home, "AppData", "Roaming"),
		"M365CRAWL_DB": "", "M365CRAWL_TEAMS_ROOT": "", "M365CRAWL_OUTLOOK_ROOT": "none",
		"M365CRAWL_ACCOUNT": "", "M365CRAWL_OUTLOOK_ACCOUNT": "", "M365CRAWL_OUTLOOK_PROFILE": "",
	} {
		t.Setenv(key, value)
	}
	name := "selected archive's evidence.db"
	if goruntime.GOOS == "windows" {
		name = "selected archive evidence.db"
	}
	a.db = filepath.Join(t.TempDir(), name)
	defaultDB, err := defaultArchivePath()
	if err != nil {
		t.Fatal(err)
	}
	b = &env{t: t, root: a.root, db: defaultDB}
	old := runSync
	runSync = func(context.Context, syncer.Options) (syncer.Report, []syncer.Change, error) {
		return syncer.Report{}, nil, errors.New("unplanned acquisition is disabled in recovery replay")
	}
	t.Cleanup(func() { runSync = old })
	fakeBrowser(t, nil)
	return a, b
}

func recoveryInvoke(args []string) (int, string, string) {
	var out, stderr strings.Builder
	code := Main(args, &out, &stderr)
	return code, out.String(), stderr.String()
}

func recoveryWords(t *testing.T, command string) []string {
	t.Helper()
	words := attendeeCommandWords(t, command)
	if len(words) < 2 || words[0] != "m365crawl" {
		t.Fatalf("not an emitted executable follow-up: %q", command)
	}
	return words
}

func recoveryValue(words []string, flag string) string {
	for i, word := range words {
		if word == flag && i+1 < len(words) {
			return words[i+1]
		}
	}
	return ""
}

func recoveryContext(t *testing.T, words []string, db, format string) {
	t.Helper()
	if got := recoveryValue(words, "--db"); got != db {
		t.Errorf("follow-up lost selected archive: got %q, want %q; argv=%q", got, db, words)
	}
	gotFormat := recoveryValue(words, "--format")
	if slices.Contains(words, "--json") {
		gotFormat = "json"
	}
	if gotFormat != format {
		t.Errorf("follow-up lost effective format: got %q, want %q; argv=%q", gotFormat, format, words)
	}
	if slices.Contains(words, "--fields") {
		t.Errorf("follow-up copied route-specific item projection: %q", words)
	}
}

func recoveryDocument(t *testing.T, out, format, label string) map[string]any {
	t.Helper()
	if format == "log" {
		var ok bool
		out, ok = strings.CutPrefix(out, label+"=")
		if !ok {
			t.Fatalf("replayed command lost log framing %s: %q", label, out)
		}
	}
	return decode(t, out)
}

func recoveryUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Errorf("follow-up changed unrelated default archive %s: %v", path, err)
	}
}

func recoveryBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Losing a one-shot --db or output format must break real replay, not just a substring assertion.
func TestRecoveryLoopRetryAndInspectStayInSelectedArchive(t *testing.T) {
	for _, tc := range []struct {
		name, format string
		flags        []string
	}{
		{"json", "json", []string{"--format", "json"}},
		{"log", "log", []string{"--format", "log"}},
		{"text", "text", []string{"--format", "text"}},
		{"json-wins", "json", []string{"--json", "--format", "text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := recoveryArchives(t)
			for _, setup := range []struct {
				e     *env
				key   string
				value any
			}{
				{a, "selected-key", nil},
				{b, "unrelated-key", loopCLIValue(t, 1, [2]string{"default", "Unrelated default text"})},
			} {
				db := loopRecords(t, setup.e)
				insertLoopCLI(t, db, setup.key, setup.value)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				runOK(t, setup.e, "--json", "loop", "sync")
			}
			args := append([]string{"--max-age", "0"}, tc.flags...)
			args = append(args, "--fields", "id", "loop", "list")
			out := runOK(t, a, args...)
			var retry string
			if tc.format == "text" {
				for _, line := range strings.Split(out, "\n") {
					if strings.Contains(line, "fix:") {
						if at := strings.LastIndex(line, "m365crawl "); at >= 0 {
							retry = strings.TrimSpace(line[at:])
						}
					}
				}
			} else {
				sources := loopCoverageSources(t, recoveryDocument(t, out, tc.format, "loop_list"))
				if len(sources) != 1 || sources[0]["source_id"] != float64(1) {
					t.Fatalf("setup must expose the selected source with colliding ID 1: %v", sources)
				}
				retry, _ = sources[0]["retry"].(string)
			}
			search := decode(t, runOK(t, a, "--json", "--max-age", "0", "search", "unmatched", "--source", "loop"))
			compact := loopCoverageSources(t, search)
			if len(compact) != 1 || compact[0]["source_id"] != float64(1) {
				t.Fatalf("compact coverage must expose selected source 1: %v", compact)
			}
			inspect, _ := compact[0]["inspect"].(string)
			before := recoveryBytes(t, b.db)
			words := recoveryWords(t, inspect)
			code, inspected, stderr := recoveryInvoke(words[1:])
			if code != 0 {
				t.Fatalf("actual inspect replay: %d %s", code, stderr)
			}
			recoveryContext(t, words, a.db, "json")
			rows := items(t, decode(t, inspected))
			if len(rows) != 1 || rows[0]["tuple"].(map[string]any)["key_json"] != "selected-key" {
				t.Errorf("actual inspect replay selected the default archive's colliding source: %s", inspected)
			}
			db, err := sql.Open("sqlite", a.db)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`update records set value_json=? where key_json='selected-key'`,
				loopCLIValue(t, 2, [2]string{"selected", "Selected archive recovered"}))
			closeErr := db.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
			words = recoveryWords(t, retry)
			code, replayed, stderr := recoveryInvoke(words[1:])
			if code != 0 {
				t.Fatalf("actual retry replay: %d %s", code, stderr)
			}
			recoveryContext(t, words, a.db, tc.format)
			if recoveryValue(words, "--source-id") != "1" || slices.Contains(words, "--account") {
				t.Errorf("retry changed the registered Loop selector: %q", words)
			}
			if tc.format == "text" {
				if strings.TrimSpace(replayed) == "" || strings.HasPrefix(strings.TrimSpace(replayed), "{") ||
					strings.HasPrefix(replayed, "loop_sync=") {
					t.Errorf("actual retry lost text framing: %q", replayed)
				}
			} else if doc := recoveryDocument(t, replayed, tc.format, "loop_sync"); doc["projected"] != float64(1) {
				t.Errorf("retry did not project selected source: %v", doc)
			}
			got := runOK(t, a, "--json", "--max-age", "0", "loop", "list")
			if !strings.Contains(got, "Selected archive recovered") || strings.Contains(got, "Unrelated default text") {
				t.Errorf("actual retry did not recover A's content: %s", got)
			}
			recoveryUnchanged(t, b.db, before)
		})
	}
}

func recoverySeedDocument(t *testing.T, e *env, kind, locator, title string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	at := time.Now().UTC()
	input := store.KnowledgeInput{Kind: kind, Platform: goruntime.GOOS, Locator: locator, At: at}
	var attempt store.KnowledgeAttempt
	if kind == store.RegistryDocumentKind {
		attempt, err = st.BeginRegistryDocumentAttempt(ctx, input)
	} else {
		attempt, err = st.BeginUsageDocumentAttempt(ctx, input)
	}
	if err != nil {
		t.Fatal(err)
	}
	if title == "" {
		err = st.RecordKnowledgeFailure(ctx, attempt, store.KnowledgeOutcome{
			Status: "failed", Reason: map[string]string{
				store.RegistryDocumentKind: "office_registry_source_busy",
				store.UsageDocumentKind:    "onedrive_usage_source_busy",
			}[kind], At: at})
	} else {
		outcome := store.KnowledgeOutcome{Status: "complete", At: at, ContentDigest: title}
		if kind == store.RegistryDocumentKind {
			outcome.Qualification = store.RegistryDocumentQualification
			err = st.CommitRegistryDocumentObservations(ctx, attempt, outcome,
				officeregistry.Result{Documents: []officeregistry.Observation{registryCLIObservation(12, title, "")}},
				store.RegistryCaptureMetadata{AcquiredAt: &at, SourceObservedAt: &at})
		} else {
			outcome.Qualification = store.UsageDocumentQualification
			err = st.CommitUsageDocumentObservations(ctx, attempt, outcome,
				onedriveusage.Result{Documents: []onedriveusage.DocumentObservation{usageCLIDocument("recovery-native", title)}},
				store.UsageCaptureMetadata{AcquiredAt: &at, SourceObservedAt: &at})
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}

func recoveryDocumentSources(t *testing.T, e *env) []store.KnowledgeSource {
	t.Helper()
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	sources, err := st.DocumentSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return sources
}

// A registered retry and an unadmitted file retry must not become each other or mutate default B.
func TestRecoveryDocumentRegisteredAndNullSelectors(t *testing.T) {
	for _, family := range []struct{ name, kind, flag string }{
		{"registry", store.RegistryDocumentKind, "--registry-file"},
		{"usage", store.UsageDocumentKind, "--onedrive-usage-file"},
	} {
		for _, registered := range []bool{false, true} {
			t.Run(family.name+"/registered="+strconv.FormatBool(registered), func(t *testing.T) {
				a, b := recoveryArchives(t)
				dir, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				name := "copy with space.reg"
				if goruntime.GOOS != "windows" {
					name = "copy's $(not-a-command).reg"
				}
				file := filepath.Join(dir, name)
				if err := os.WriteFile(file, []byte("synthetic acquisition is replaced"), 0o600); err != nil {
					t.Fatal(err)
				}
				if registered {
					recoverySeedDocument(t, a, family.kind, file, "Selected before failure")
				}
				recoverySeedDocument(t, a, family.kind, file, "")
				recoverySeedDocument(t, b, family.kind, file, "Unrelated default document")
				run := runDocumentsSync
				runDocumentsSync = func(_ context.Context, o syncer.DocumentOptions) (syncer.DocumentReport, error) {
					target := &env{t: t, db: o.DBPath}
					if o.Explicit {
						locator, kind := o.RegistryFile, store.RegistryDocumentKind
						if o.UsageFile != "" {
							locator, kind = o.UsageFile, store.UsageDocumentKind
						}
						if o.SourceID > 0 {
							locator = ""
							for _, source := range recoveryDocumentSources(t, target) {
								if source.ID == o.SourceID {
									locator, kind = source.Locator, source.Kind
								}
							}
						}
						if locator == "" || o.Root != "" {
							return syncer.DocumentReport{}, store.ErrKnowledgeSourceUnknown
						}
						recoverySeedDocument(t, target, kind, locator, "Selected document recovered")
					}
					return syncer.DocumentReport{Captured: 1, Sources: recoveryDocumentSources(t, target)}, nil
				}
				t.Cleanup(func() { runDocumentsSync = run })
				doc := decode(t, runOK(t, a, "--json", "--max-age", "0", "docs", "sources"))
				sources := coverageSources(t, doc)
				if len(sources) != 1 || (sources[0]["source_id"] != nil) != registered {
					t.Fatalf("setup did not establish registered/null distinction: %v", sources)
				}
				fix, _ := sources[0]["fix"].(string)
				at := strings.LastIndex(fix, "m365crawl ")
				if at < 0 {
					t.Fatalf("no retry in source failure: %q", fix)
				}
				words := recoveryWords(t, fix[at:])
				before := recoveryBytes(t, b.db)
				code, out, stderr := recoveryInvoke(words[1:])
				if code != 0 {
					t.Fatalf("actual document retry: %d %s", code, stderr)
				}
				recoveryContext(t, words, a.db, "json")
				if registered {
					if recoveryValue(words, "--source-id") != "1" || slices.Contains(words, family.flag) {
						t.Errorf("registered retry lost its exclusive source ID: %q", words)
					}
				} else if recoveryValue(words, family.flag) != file || slices.Contains(words, "--source-id") {
					t.Errorf("null-source retry invented an ID or changed the literal filename: %q", words)
				}
				if recoveryDocument(t, out, "json", "docs_sync")["captured"] != float64(1) {
					t.Errorf("actual capture result not returned: %s", out)
				}
				list := runOK(t, a, "--json", "--max-age", "0", "docs", "list")
				if !strings.Contains(list, "Selected document recovered") {
					t.Errorf("document retry did not admit recovered content in A: %s", list)
				}
				recoveryUnchanged(t, b.db, before)
			})
		}
	}
}

func recoveryLinkApplied(t *testing.T, db string) bool {
	t.Helper()
	st, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ok, err := st.OutlookLinkInEffect(context.Background(), "outlook/Main", trAccount)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestRecoveryCommandPolicyBoundaries(t *testing.T) {
	wd := t.TempDir()
	old := getwd
	getwd = func() (string, error) { return wd, nil }
	t.Cleanup(func() { getwd = old })
	rt := &runtime{dbPath: "selected archive.db", outlookOn: true}
	words := recoveryWords(t, rt.recoveryCommand(true, "", "docs", "sync"))
	want := []string{"m365crawl", "--db", filepath.Join(wd, "selected archive.db"),
		"--max-age", "0", "--format", "json", "docs", "sync"}
	if !slices.Equal(words, want) {
		t.Errorf("default format, empty roots or relative archive changed argv: got %q want %q", words, want)
	}
	for _, tc := range []struct{ locator, want string }{
		{"", `""`},
		{`C:\cache\usage.db`, `C:\cache\usage.db`},
		{`C:\cache notes\usage.db`, `"C:\cache notes\usage.db"`},
	} {
		if got := fileRetryArgument("windows", tc.locator); got != tc.want {
			t.Errorf("Windows single-argument policy for %q: got %q want %q", tc.locator, got, tc.want)
		}
	}
	unsafe := `C:\cache\%USERNAME%.db`
	if got := fileRetryArgument("windows", unsafe); strings.Contains(got, unsafe) || !strings.HasPrefix(got, "FILE, where FILE") {
		t.Errorf("shell-expanding locator was not withheld: %q", got)
	}
	locator := "owned archive's cache $(not-a-command).db"
	words = attendeeCommandWords(t, "m365crawl --file "+fileRetryArgument("darwin", locator))
	if !slices.Equal(words, []string{"m365crawl", "--file", locator}) {
		t.Errorf("POSIX literal argument changed: %q", words)
	}
}

// Link source roots and transcript account scope are operation inputs, not presentation-only hints.
func TestRecoveryExplicitLinkAndTranscriptContext(t *testing.T) {
	t.Run("stable-relative-roots-and-refusal", func(t *testing.T) {
		a, _ := recoveryArchives(t)
		wd := t.TempDir()
		old := getwd
		getwd = func() (string, error) { return wd, nil }
		t.Cleanup(func() { getwd = old })
		globals := &Globals{DB: a.db, TeamsRoot: "teams copy", OutlookRoot: "outlook copy", Format: "log", MaxAge: "0"}
		rt := newRuntime(context.Background(), globals, &strings.Builder{}, &strings.Builder{})
		setup := func() {
			t.Helper()
			if err := rt.setup(); err != nil {
				t.Fatal(err)
			}
		}
		setup()
		if rt.root != "teams copy" || rt.outlookRoot != "outlook copy" {
			t.Fatal("the regression must exercise actual relative roots retained by runtime setup")
		}
		words := recoveryWords(t, rt.recoveryCommand(true, "", "sync"))
		recoveryContext(t, words, a.db, "log")
		for flag, want := range map[string]string{"--teams-root": filepath.Join(wd, "teams copy"),
			"--outlook-root": filepath.Join(wd, "outlook copy")} {
			if recoveryValue(words, flag) != want {
				t.Errorf("relative root changed destination: %s want %q, argv=%q", flag, want, words)
			}
		}
		globals.TeamsRoot, globals.OutlookRoot, globals.Format = "none", "none", "text"
		setup()
		words = recoveryWords(t, rt.recoveryCommand(true, "", "sync"))
		recoveryContext(t, words, a.db, "text")
		if recoveryValue(words, "--teams-root") != filepath.Join(wd, "none") ||
			recoveryValue(words, "--outlook-root") != "none" {
			t.Errorf("Outlook none must remain a sentinel, not change the relative Teams folder meaning: %q", words)
		}
		getwd = func() (string, error) { return "", errors.New("synthetic cwd lookup failure") }
		refused := func(flag string) {
			t.Helper()
			command := rt.recoveryCommand(true, "", "sync")
			if strings.HasPrefix(command, "m365crawl ") || !strings.Contains(command, "absolute "+flag) {
				t.Errorf("unknown originating directory must produce explicit non-executable %s guidance: %q", flag, command)
			}
		}
		globals.TeamsRoot = "relative teams"
		setup()
		refused("--teams-root")
		globals.TeamsRoot, globals.OutlookRoot = filepath.Join(wd, "absolute teams"), "relative outlook"
		setup()
		refused("--outlook-root")
		globals.DB, globals.OutlookRoot = "relative archive.db", "none"
		setup()
		refused("--db")
		globals.DB, globals.TeamsRoot, globals.OutlookRoot, globals.JSON = a.db, "relative teams", "relative outlook", true
		setup()
		words = recoveryWords(t, rt.recoveryCommand(false, trAccount, "transcripts", "fetch", "event-target"))
		recoveryContext(t, words, a.db, "json")
		if recoveryValue(words, "--account") != trAccount || !slices.Contains(words, "event-target") ||
			slices.Contains(words, "--teams-root") || slices.Contains(words, "--outlook-root") {
			t.Errorf("archive-only follow-up must not inherit acquisition roots or lose account/target: %q", words)
		}
	})
	t.Run("pending-link", func(t *testing.T) {
		a, b := recoveryArchives(t)
		seedTranscripts(t, a)
		seedTranscripts(t, b)
		a.root = teamsRoot(t)
		root := t.TempDir()
		code, _, stderr := a.run("--json", "--format", "text", "--max-age", "0", "--outlook-root", root,
			"--outlook-profile", "Main", "--outlook-account", trAccount, "calendar", "--days", "1")
		if code != 2 {
			t.Fatalf("expected actual pending-link refusal, got %d: %s", code, stderr)
		}
		fix := errorOf(t, stderr)["fix"].(string)
		parts := strings.Split(fix, "`")
		if len(parts) < 3 {
			t.Fatalf("pending-link fix lost executable guidance: %q", fix)
		}
		words := recoveryWords(t, parts[1])
		old := runSync
		runSync = func(ctx context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
			if o.Root != a.root || o.OutlookRoot != root || !o.OutlookEnabled ||
				o.OutlookLinkProfile != "Main" || o.OutlookLink != trAccount {
				return syncer.Report{}, nil, errors.New("replayed sync did not retain selected roots/profile/principal")
			}
			st, err := store.Open(ctx, o.DBPath)
			if err != nil {
				return syncer.Report{}, nil, err
			}
			defer func() { _ = st.Close() }()
			err = st.SetOutlookLink(ctx, "outlook/Main", o.OutlookLink, trFetchedAt)
			return syncer.Report{Status: syncer.StatusOK}, nil, err
		}
		t.Cleanup(func() { runSync = old })
		before := recoveryBytes(t, b.db)
		code, out, stderr := recoveryInvoke(words[1:])
		if code != 0 {
			t.Errorf("actual emitted link did not execute in selected context: %d %s", code, stderr)
		} else {
			recoveryDocument(t, out, "json", "sync")
		}
		recoveryContext(t, words, a.db, "json")
		for flag, want := range map[string]string{"--teams-root": a.root, "--outlook-root": root,
			"--outlook-profile": "Main", "--outlook-account": trAccount} {
			if recoveryValue(words, flag) != want {
				t.Errorf("link lost %s=%q: %q", flag, want, words)
			}
		}
		if !recoveryLinkApplied(t, a.db) || recoveryLinkApplied(t, b.db) {
			t.Error("actual link was not applied exclusively in selected A")
		}
		recoveryUnchanged(t, b.db, before)
	})
	t.Run("account-scoped-transcript", func(t *testing.T) {
		a, b := recoveryArchives(t)
		seedTranscripts(t, a)
		seedTranscripts(t, b)
		other := recoveryOtherTranscriptAccount(t, a)
		trSharePointHosts(a)
		fakeBrowser(t, newTrPage())
		doc := decode(t, runOK(t, a, "--json", "--max-age", "0", "--account", trAccount,
			"--fields", "event_id,transcripts", "calendar", "event", trEventID()))
		var command string
		for _, notice := range noticesOf(doc) {
			if at := strings.Index(notice, "m365crawl "); at >= 0 {
				command = notice[at:]
			}
		}
		words := recoveryWords(t, command)
		before := recoveryBytes(t, b.db)
		code, out, stderr := recoveryInvoke(words[1:])
		if code != 0 {
			t.Fatalf("actual emitted transcript fetch failed: %d %s", code, stderr)
		}
		recoveryContext(t, words, a.db, "json")
		if recoveryValue(words, "--account") != trAccount || !slices.Contains(words, trEventID()) {
			t.Errorf("transcript follow-up lost resolved account/event: %q", words)
		}
		if recoveryDocument(t, out, "json", "transcripts_fetch")["fetched"] != float64(1) {
			t.Errorf("fetch was not restricted to selected account's missing part: %s", out)
		}
		if !recoveryPartFetched(t, a.db, trAccount) || recoveryPartFetched(t, a.db, other) ||
			recoveryPartFetched(t, b.db, trAccount) {
			t.Error("actual transcript replay crossed account/archive scope or failed to save A's missing part")
		}
		recoveryUnchanged(t, b.db, before)
	})
}

func recoveryOtherTranscriptAccount(t *testing.T, e *env) string {
	t.Helper()
	otherTenant := "00000000-0000-4000-8000-000000000002"
	otherUser := "00000000-0000-4000-8000-0000000000a2"
	other := otherTenant + "/" + otherUser
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, query := range []string{
		`insert into conversations(tenant_id,user_id,id,kind,title,display_name,updated_at)
select ?,?,id,kind,title,display_name,updated_at from conversations where tenant_id=? and user_id=?`,
		`insert into messages(tenant_id,user_id,conversation_id,id,sent_at,message_type,content_html,updated_at)
select ?,?,conversation_id,id,sent_at,message_type,content_html,updated_at from messages where tenant_id=? and user_id=?`,
	} {
		if _, err := db.Exec(query, otherTenant, otherUser, tenantA, userA); err != nil {
			t.Fatal(err)
		}
	}
	event := trEvent()
	event.AccountID = other
	window := calendar.Window{Source: calendar.SourceTeams, AccountID: other,
		Start: event.Start.Add(-time.Hour), End: event.End.Add(time.Hour), SyncedAt: trFetchedAt, CacheFreshAt: trFetchedAt}
	if _, err := calendar.ApplySnapshot(context.Background(), db, window, []calendar.Event{event}, trFetchedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	sess, err := st.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Rollback()
	if _, err := sess.DeriveTranscriptParts(context.Background(), []string{other}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Commit(); err != nil {
		t.Fatal(err)
	}
	return other
}

func recoveryInstruction(t *testing.T, text string) []string {
	t.Helper()
	at := strings.Index(text, "m365crawl ")
	if at < 0 {
		t.Fatalf("qualification: actual output contains no executable remedy: %q", text)
	}
	command, _, _ := strings.Cut(text[at:], "\n")
	command, _, _ = strings.Cut(command, ", and ")
	return recoveryWords(t, strings.TrimSuffix(strings.TrimSpace(command), " again"))
}

func TestRecoveryLoopBatchContinuationReplaysSelectedArchive(t *testing.T) {
	for _, format := range []string{"json", "log", "text"} {
		t.Run(format, func(t *testing.T) {
			a, b := recoveryArchives(t)
			db := loopRecords(t, b)
			insertLoopCLI(t, db, "default-only", loopCLIValue(t, 1, [2]string{"default", "Unrelated default text"}))
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			runOK(t, b, "--json", "loop", "sync")
			before := recoveryBytes(t, b.db)
			db = loopRecords(t, a)
			for i := 0; i < 129; i++ {
				insertLoopCLI(t, db, fmt.Sprintf("selected-%03d", i),
					loopCLIValue(t, 1, [2]string{"selected", fmt.Sprintf("Selected channel %03d", i)}))
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			first := runOK(t, a, "--format", format, "loop", "sync")
			var notice string
			if format == "text" {
				at := strings.Index(first, "still unregistered: run ")
				if at < 0 {
					t.Fatalf("qualification: actual batch notice absent: %q", first)
				}
				notice = first[at:]
			} else {
				doc := recoveryDocument(t, first, format, "loop_sync")
				if doc["projected"] != float64(128) || doc["discovery_remaining"] != true ||
					doc["discovery_remaining_records"] != float64(1) {
					t.Fatalf("qualification: did not reach actual128+1 batch: %v", doc)
				}
				notice, _ = doc["notice"].(string)
			}
			registered := decode(t, runOK(t, a, "--json", "--max-age", "0", "loop", "sources"))
			if len(items(t, registered)) != 128 {
				t.Fatal("qualification: first actual batch did not register exactly128 sources")
			}
			words := recoveryInstruction(t, notice)
			code, _, stderr := recoveryInvoke(words[1:])
			if code != 0 {
				t.Fatalf("actual batch continuation replay: %d %s", code, stderr)
			}
			recoveryContext(t, words, a.db, format)
			final := decode(t, runOK(t, a, "--json", "--max-age", "0", "loop", "sources"))
			if len(items(t, final)) != 129 {
				t.Errorf("actual_continuation_stranded_selected_backlog: count=%d", len(items(t, final)))
			}
			recoveryUnchanged(t, b.db, before)
		})
	}
}

func TestRecoveryRuntimeHintAndEmptyNoteReplayAcquisitionContext(t *testing.T) {
	for _, state := range []string{"missing", "never-successful", "missing-account"} {
		for _, tc := range []struct {
			name, format string
			flags        []string
		}{
			{"json", "json", []string{"--format", "json"}},
			{"log", "log", []string{"--format", "log"}},
			{"text", "text", []string{"--format", "text"}},
			{"json-wins", "json", []string{"--json", "--format", "text"}},
		} {
			t.Run(state+"/"+tc.name, func(t *testing.T) {
				a, b := recoveryArchives(t)
				db := loopRecords(t, b)
				insertLoopCLI(t, db, "default-only", loopCLIValue(t, 1, [2]string{"default", "Unrelated default text"}))
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if state == "never-successful" {
					db = loopRecords(t, a)
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
				}
				beforeB := recoveryBytes(t, b.db)
				var beforeA []byte
				if state == "never-successful" {
					beforeA = recoveryBytes(t, a.db)
				}
				oldSync, readHere := runSync, teamsReadHere
				t.Cleanup(func() { runSync, teamsReadHere = oldSync, readHere })
				teamsReadHere = func() bool { return true }
				calls := 0
				runSync = func(ctx context.Context, o syncer.Options) (syncer.Report, []syncer.Change, error) {
					calls++
					if o.DBPath != a.db || o.Root != a.root || o.OutlookEnabled || o.OutlookRoot != "" {
						t.Errorf("actual_remedy_lost_acquisition_context: %+v", o)
					}
					if state == "missing-account" && (o.Account == nil || o.Account.TenantID+"/"+o.Account.UserID != trAccount) {
						t.Errorf("actual remedy broadened explicit account: %+v", o.Account)
					}
					st, err := store.Open(ctx, o.DBPath)
					if err != nil {
						return syncer.Report{}, nil, err
					}
					if err := st.Close(); err != nil {
						return syncer.Report{}, nil, err
					}
					return syncer.Report{Status: syncer.StatusOK}, nil, nil
				}
				args := append([]string{"--max-age", "0", "--outlook-root", "none"}, tc.flags...)
				if state == "missing-account" {
					args = append(args, "--account", trAccount)
				}
				args = append(args, "people")
				code, out, stderr := a.run(args...)
				if code != 0 || calls != 0 {
					t.Fatalf("archive-only initial read acquired or failed: %d calls=%d %s", code, calls, stderr)
				}
				var hint, note string
				if tc.format == "text" {
					hint, note = stderr, out
				} else {
					doc := recoveryDocument(t, out, tc.format, "people")
					age, present := doc["archive_age_seconds"]
					if !present || age != nil || doc["needs_sync"] != true || doc["sync_error"] != nil {
						t.Fatalf("archive-only read invented freshness/error or lost needs_sync: %v", doc)
					}
					hint, _ = doc["hint"].(string)
					note, _ = doc["note"].(string)
				}
				if state != "never-successful" {
					if _, err := os.Stat(a.db); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("initial missing-archive read created A: %v", err)
					}
				} else {
					recoveryUnchanged(t, a.db, beforeA)
				}
				recoveryUnchanged(t, b.db, beforeB)
				for _, remedy := range []string{hint, note} {
					words := recoveryInstruction(t, remedy)
					code, replayed, stderr := recoveryInvoke(words[1:])
					if code != 0 {
						t.Fatalf("actual in-band remedy replay failed: %d %s", code, stderr)
					}
					recoveryContext(t, words, a.db, tc.format)
					if state == "missing-account" && recoveryValue(words, "--account") != trAccount {
						t.Errorf("actual hint/note lost explicit account: %q", words)
					}
					if tc.format != "text" {
						recoveryDocument(t, replayed, tc.format, "sync")
					}
					for flag, want := range map[string]string{"--teams-root": a.root, "--outlook-root": "none"} {
						if recoveryValue(words, flag) != want {
							t.Errorf("actual remedy lost %s=%q: %q", flag, want, words)
						}
					}
				}
				if calls != 2 {
					t.Errorf("both actual in-band remedies must reach recording acquisition boundary: %d", calls)
				}
				if _, err := os.Stat(a.db); err != nil {
					t.Errorf("actual remedies did not address selected A: %v", err)
				}
				recoveryUnchanged(t, b.db, beforeB)
			})
		}
	}
}
func recoveryPartFetched(t *testing.T, path, account string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`select count(*) from transcript_fetches where account_id=? and part_key='d:b!d1/P3' and state='ok'`, account).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}
