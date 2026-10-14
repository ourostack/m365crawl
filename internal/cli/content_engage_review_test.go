package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/documenttext"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

// A wrong final-row schema must not panic, relabel earlier cells, or lose bodies.
func TestEngageReviewHeterogeneousTables(t *testing.T) {
	local := LocalTextItem{ID: "local-text-observation:1", FileLabel: "synthetic.docx", RequestedKind: "docx", State: "observations",
		Paragraphs: []documenttext.Paragraph{{Text: "LOCAL_BODY"}}}
	engage := EngageContentItem{ID: "engage-observation:2", SourceID: 2, Blocks: []string{"ENGAGE_BODY"}}
	reg := engageRegistrationItem{RegistrationID: 3, Outcome: "unavailable", AccountState: "unknown", Reason: "engage_unavailable", Retry: "synthetic-retry"}
	instance := engageSourceItem{RegistrationID: 3, SourceID: 4, CurrentSessionID: 5, Coverage: "native_home_observations", IsLastSuccessfulInstance: true}
	for _, tc := range []struct {
		name  string
		items []any
		want  []string
	}{
		{"local-engage", []any{local, engage}, []string{"file_modified_at", "starter_updated_at", "LOCAL_BODY", "ENGAGE_BODY"}},
		{"engage-local", []any{engage, local}, []string{"starter_updated_at", "file_modified_at", "ENGAGE_BODY", "LOCAL_BODY"}},
		{"registration-instance", []any{reg, instance}, []string{"account_state", "reason", "retry", "current_session", "last_successful_instance"}},
		{"cloud-local-engage", []any{cloudSourceItem{RegistrationID: 1}, localSourceItem{RegistrationID: 2}, reg, instance}, []string{"selection", "snapshot_match", "account_state", "current_session"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("actual renderer panicked: %v", p)
				}
			}()
			var out bytes.Buffer
			rt := &runtime{stdout: &out, g: &Globals{}, cmd: "content list"}
			rt.listTable(newList(tc.items, false))
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in actual table:\n%s", want, out.String())
				}
			}
			if strings.Contains(tc.name, "instance") {
				assertEngageReviewTable(t, out.String(), []string{"registration", "outcome", "account_state", "reason", "retry"},
					[]string{"3", "unavailable", "unknown", "engage_unavailable", "synthetic-retry"})
				assertEngageReviewTable(t, out.String(), []string{"registration", "source", "current_session", "coverage", "last_successful_instance"},
					[]string{"3", "4", "5", "native_home_observations", "true"})
			}
		})
	}
}

func assertEngageReviewTable(t *testing.T, out string, header, row []string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if reflect.DeepEqual(strings.Fields(line), header) {
			for _, next := range lines[i+1:] {
				cells := strings.Fields(next)
				if len(cells) == len(row) && cells[0] == row[0] {
					if !reflect.DeepEqual(cells, row) {
						t.Errorf("cells relabeled under %v: got %v want %v", header, cells, row)
					}
					return
				}
			}
			t.Errorf("no row %v under %v", row, header)
			return
		}
	}
	t.Errorf("no truthful header %v:\n%s", header, out)
}

func engageReviewLocal(t *testing.T, e *env, unknown ...bool) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	input := officecapture.TextInput{Path: "/synthetic/review.docx", RequestedKind: "docx"}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a, err := st.BeginLocalTextAttempt(ctx, store.KnowledgeInput{Kind: store.LocalTextKind, Platform: "darwin", Locator: store.LocalTextLocator(input), At: at}, input)
	if err != nil {
		t.Fatal(err)
	}
	raw, gen, creation, kind := strconv.FormatInt(at.Unix(), 10)+":0", "7", "1:0", "docx"
	modified := &at
	if len(unknown) > 0 && unknown[0] {
		// Resident legacy evidence retains raw year-one but parsed absence.
		raw, modified = "-62135596800:0", nil
	}
	c := store.LocalTextCapture{RequestedKind: kind, VerifiedKind: &kind, SourceByteDigest: strings.Repeat("a", 64), AcquiredAt: at,
		Result: documenttext.Result{Kind: kind, State: "text_observations",
			Paragraphs: []documenttext.Paragraph{{Part: "word/document.xml", Index: 0, Text: "syntheticword LOCAL"}}},
		Evidence: officecapture.FileEvidence{VolumeID: "1", FileID: "2", GenerationKind: "native_generation", GenerationPolicy: officecapture.FileGenerationPolicy,
			NativeGenerationRaw: &gen, CreationRaw: &creation, ModificationRaw: &raw, ModifiedAt: modified, GenerationQualified: true, Size: 8, NativeAttributes: "0"}}
	if err := st.CommitLocalText(ctx, a, store.LocalTextCommit{Capture: c, RequestOrigin: "explicit"}); err != nil {
		t.Fatal(err)
	}
}

func engageReviewFetch(t *testing.T, e *env, raw string) {
	t.Helper()
	engageControlled(t, &engageFixturePage{})
	saved := observeEngageHome
	t.Cleanup(func() { observeEngageHome = saved })
	observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
		r := engageCLIResult("review-synthetic")
		r.Threads[0].Blocks = []string{"syntheticword ENGAGE"}
		if raw != "" {
			at, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				t.Fatal(err)
			}
			r.Threads[0].StarterUpdatedAt, r.Threads[0].StarterUpdatedRaw = &at, &raw
			r.Losses[0].Count = 3
		}
		return r, nil
	}
	if code, _, stderr := e.run("content", "fetch", "engage-home"); code != 0 {
		t.Fatal(stderr)
	}
}

func engageReviewCloud(t *testing.T, e *env, raw string) {
	t.Helper()
	closer := installCloudFixture(t, nil)
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(cloudFixtureRaw, "searchableword", "syntheticword")), &payload); err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		payload["file"].(map[string]any)["ModifiedRaw"] = raw
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		return &cloudFixturePage{raw: string(encoded), host: "fixture.sharepoint.com"}, closer, nil
	}
	if code, _, stderr := e.run("content", "fetch", "page", "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"); code != 0 {
		t.Fatal(stderr)
	}
}

func TestEngageReviewMixedCLITextAndJSON(t *testing.T) {
	for _, raw := range []string{"2025-01-01T00:00:00Z", "2027-01-01T00:00:00Z"} {
		t.Run(raw, func(t *testing.T) {
			e := cloudCLIEnv(t)
			engageReviewLocal(t, e)
			engageReviewFetch(t, e, raw)
			for _, args := range [][]string{{"content", "list"}, {"content", "search", "syntheticword"}, {"search", "syntheticword", "--source", "content"}} {
				t.Run(strings.Join(args, "-"), func(t *testing.T) {
					defer func() {
						if p := recover(); p != nil {
							t.Errorf("actual CLI text panicked: %v", p)
						}
					}()
					code, out, stderr := e.run(append([]string{"--format", "text"}, args...)...)
					if code != 0 || !strings.Contains(out, "syntheticword LOCAL") || !strings.Contains(out, "syntheticword ENGAGE") {
						t.Fatalf("mixed bodies lost: %d %s %s", code, out, stderr)
					}
					code, out, stderr = e.run(args...)
					if code != 0 {
						t.Fatal(stderr)
					}
					got := items(t, decode(t, out))
					want := "local-text-observation:1"
					if raw == "2027-01-01T00:00:00Z" {
						want = "engage-observation:1"
					}
					if len(got) != 2 || got[0]["id"] != want {
						t.Fatalf("mixed JSON/order: %s", out)
					}
				})
			}
		})
	}
}

func TestEngageReviewSourceCLILabels(t *testing.T) {
	e := cloudCLIEnv(t)
	engageReviewCloud(t, e, "")
	engageReviewLocal(t, e)
	engageReviewFetch(t, e, "")
	for _, failed := range []bool{false, true} {
		if failed {
			observeEngageHome = func(context.Context, engagecontent.Driver) (engagecontent.Result, error) {
				return engagecontent.Result{}, &engagecontent.ReadError{Code: "identity_missing"}
			}
			if code, _, _ := e.run("content", "fetch", "engage-home"); code == 0 {
				t.Fatal("unknown failure admitted")
			}
		}
		t.Run(strconv.FormatBool(failed), func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("actual sources text panicked: %v", p)
				}
			}()
			code, out, stderr := e.run("--format", "text", "content", "sources")
			if code != 0 {
				t.Fatal(stderr)
			}
			for _, label := range []string{"selection", "snapshot_match", "account_state", "reason", "retry", "current_session", "last_successful_instance"} {
				if !strings.Contains(out, label) {
					t.Errorf("missing truthful %s:\n%s", label, out)
				}
			}
			// Inspect cells under their own headers, not the coverage footer.
			reg := engageReviewTableCells(t, out, []string{"registration", "outcome", "account_state", "reason", "retry"}, "3")
			outcome, accountState, reason := "partial", "verified_at_last_success", "-"
			if failed {
				outcome, accountState, reason = "failed", "unknown", "engage_identity_unverified"
			}
			if reg["outcome"] != outcome || reg["account_state"] != accountState || reg["reason"] != reason ||
				!strings.HasPrefix(reg["retry"], "m365crawl --db ") {
				t.Errorf("registration cells misattributed: %v", reg)
			}
			instance := engageReviewTableCells(t, out, []string{"registration", "source", "current_session", "coverage", "last_successful_instance"}, "3")
			if instance["source"] != "3" || instance["current_session"] != "1" ||
				instance["coverage"] != "native_home_observations" || instance["last_successful_instance"] != "true" {
				t.Errorf("instance cells changed after unknown failure: %v", instance)
			}
			code, out, stderr = e.run("content", "sources")
			if code != 0 || len(items(t, decode(t, out))) != 4 {
				t.Fatalf("source JSON changed: %s %s", out, stderr)
			}
			if failed && !strings.Contains(out, `"account_state":"unknown"`) {
				t.Fatal("unknown attribution hidden")
			}
		})
	}
}

func engageReviewTableCells(t *testing.T, out string, header []string, first string) map[string]string {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if !reflect.DeepEqual(strings.Fields(line), header) {
			continue
		}
		offsets := make([]int, len(header)+1)
		for j, label := range header {
			offsets[j] = strings.Index(line, label)
		}
		for _, row := range lines[i+1:] {
			if len(row) < offsets[1] || strings.TrimSpace(row[:offsets[1]]) != first {
				continue
			}
			offsets[len(header)] = len(row)
			cells := map[string]string{}
			for j, label := range header {
				cells[label] = strings.TrimSpace(row[min(offsets[j], len(row)):min(offsets[j+1], len(row))])
			}
			return cells
		}
	}
	t.Fatalf("missing row %s under exact header %v:\n%s", first, header, out)
	return nil
}
