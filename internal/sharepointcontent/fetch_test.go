package sharepointcontent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func stringPointer(value string) *string { return &value }

func nativeResult() scriptResult {
	return scriptResult{
		State: "metadata_only", HTTPStatus: 200,
		Account: SourceAccount{Host: "fixture.sharepoint.com", WebID: "cccccccc-cccc-cccc-cccc-cccccccccccc", ID: 11, LoginName: "fixture-account"},
		File: NativeFile{SiteID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", WebID: "cccccccc-cccc-cccc-cccc-cccccccccccc", FileID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			Path: "/sites/sample/SitePages/Page.aspx", ModifiedRaw: stringPointer("2026-01-01T00:00:00Z")},
	}
}

type capturePage struct {
	landingPage
	result  scriptResult
	evalErr error
	cancel  context.CancelFunc
	expr    string
}

func (p *capturePage) Eval(context context.Context, expr string, out any) error {
	p.calls = append(p.calls, "eval")
	p.expr = expr
	if p.cancel != nil {
		p.cancel()
	}
	if p.evalErr != nil {
		return p.evalErr
	}
	*out.(*scriptResult) = p.result
	return nil
}

func TestFetchNativeMetadataAndSafeFatalStates(t *testing.T) {
	request := Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"}
	for _, test := range []struct {
		name   string
		mutate func(*scriptResult)
		code   string
	}{
		{"metadata", func(*scriptResult) {}, ""},
		{"wrong-host", func(r *scriptResult) { r.Account.Host = "other.sharepoint.com" }, "identity_mismatch"},
		{"wrong-web", func(r *scriptResult) { r.Account.WebID = "dddddddd-dddd-dddd-dddd-dddddddddddd" }, "identity_mismatch"},
		{"wrong-path", func(r *scriptResult) { r.File.Path = "/other/path" }, "identity_mismatch"},
		{"invalid-native-id", func(r *scriptResult) { r.File.FileID = "not-a-native-id" }, "malformed"},
		{"absent-account", func(r *scriptResult) { r.Account.ID = 0 }, "malformed"},
		{"unknown-state", func(r *scriptResult) { r.State = "looks-successful" }, "malformed"},
		{"oversized-account", func(r *scriptResult) { r.Account.LoginName = strings.Repeat("x", 262145) }, "too_large"},
		{"invalid-loss", func(r *scriptResult) { r.Losses = []Loss{{Code: "private-value", Count: 1}} }, "malformed"},
		{"denied-file", func(r *scriptResult) { *r = scriptResult{State: "no_access", HTTPStatus: 403} }, "no_access"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := nativeResult()
			test.mutate(&raw)
			page := &capturePage{landingPage: landingPage{host: "fixture.sharepoint.com"}, result: raw}
			got, err := Fetch(context.Background(), page, request)
			if test.code == "" {
				if err != nil || got.State != "metadata_only" || got.Kind != "page" || got.File.ModifiedAt == nil || got.File.ModifiedAt.Year() != 2026 ||
					!reflect.DeepEqual(page.calls, []string{"navigate", "host", "eval"}) || !strings.Contains(page.expr, "GetFileByServerRelativePath") {
					t.Fatalf("native metadata was not captured: %#v,%v; calls=%v", got, err, page.calls)
				}
			} else {
				var safe *ReadError
				if !errors.As(err, &safe) || safe.Code != test.code || !reflect.DeepEqual(got, Result{}) {
					t.Fatalf("unsafe fatal publication: %#v,%v", got, err)
				}
			}
		})
	}
}

func TestFetchLateCancelAndDriverFailurePublishNothing(t *testing.T) {
	request := Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	page := &capturePage{landingPage: landingPage{host: "fixture.sharepoint.com"}, result: nativeResult(), cancel: cancel}
	if got, err := Fetch(ctx, page, request); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("late cancelled capture published: %#v,%v", got, err)
	}

	page = &capturePage{landingPage: landingPage{host: "fixture.sharepoint.com"}, evalErr: errors.New("private-driver-content")}
	got, err := Fetch(context.Background(), page, request)
	var safe *ReadError
	if !errors.As(err, &safe) || safe.Code != "unreadable" || strings.Contains(err.Error(), "private-driver-content") || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("driver failure leaked/published: %#v,%v", got, err)
	}
}

func TestFetchPageControlsAreValidatedAndOwned(t *testing.T) {
	raw := nativeResult()
	raw.State = "page_observations"
	id := "dddddddd-dddd-dddd-dddd-dddddddddddd"
	kind := int64(4)
	raw.PageControls = []PageControl{{Ordinal: 0, ID: &id, Type: &kind, State: "text", Texts: []string{"native text"}}}
	raw.Losses = []Loss{{Code: "control_text_unmapped", Count: 1}}
	page := &capturePage{landingPage: landingPage{host: "fixture.sharepoint.com"}, result: raw}
	request := Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"}
	got, err := Fetch(context.Background(), page, request)
	if err != nil || len(got.PageControls) != 1 || !got.Partial || got.PageControls[0].Texts[0] != "native text" {
		t.Fatalf("valid native page refused: %#v,%v", got, err)
	}
	id = "mutated"
	kind = 3
	page.result.PageControls[0].Texts[0] = "mutated"
	*page.result.File.ModifiedRaw = "mutated"
	if *got.PageControls[0].ID != "dddddddd-dddd-dddd-dddd-dddddddddddd" || *got.PageControls[0].Type != 4 ||
		got.PageControls[0].Texts[0] != "native text" || *got.File.ModifiedRaw != "2026-01-01T00:00:00Z" {
		t.Fatal("caller-owned script result mutated published output")
	}
}
