package sharepointcontent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestReviewNativeSiteAPIEncoding(t *testing.T) {
	ctx, page := scriptPage(t)
	for _, site := range []string{"A%23B", "A%3FB", "A%252FB", "%252e%252e", "A%20B%25"} {
		admitted, err := admit(Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/" + site + "/SitePages/Page.aspx"})
		if err != nil {
			t.Fatal(err)
		}
		args, _ := json.Marshal(admitted)
		expression := `(async args=>{
			const location={origin:"https://fixture.sharepoint.com",hostname:"fixture.sharepoint.com"};
			const payloads=[{UniqueId:"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",ServerRelativeUrl:args.Path},
				{Id:"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},{Id:"cccccccc-cccc-cccc-cccc-cccccccccccc"},{Id:11,LoginName:"fixture-account"}];
			const paths=[];
			const fetch=async url=>{
				const response=new Response(JSON.stringify(payloads[paths.length]),{headers:{"content-type":"application/json"}});
				paths.push(decodeURIComponent(new URL(url).pathname));Object.defineProperty(response,"url",{value:String(url)});return response;
			};
			await (` + captureJS + `)(args,(` + networkJS + `));
			return paths;
		})(` + string(args) + `)`
		var paths []string
		if err := page.Eval(ctx, expression, &paths); err != nil {
			t.Fatal("synthetic encoded-site evaluation failed")
		}
		if len(paths) != 4 || paths[2] != admitted.Site+"/_api/web" {
			t.Errorf("%s site changed during native API serialization: %q", site, paths)
		}
	}
}

func TestReviewRichTextEligibilityAndLiteralBoundaries(t *testing.T) {
	ctx, page := scriptPage(t)
	for _, test := range []struct {
		name, inside string
		want         []string
	}{
		{"foreign-root", `<div data-sp-rte>Safe</div><svg><foreignObject><div data-sp-rte>FOREIGN</div></foreignObject></svg>`, []string{"Safe"}},
		{"nested-foreign-root", `<div data-sp-rte>Safe<svg><foreignObject><div data-sp-rte>FOREIGN</div></foreignObject></svg></div>`, []string{"Safe"}},
		{"block-entry", `<div data-sp-rte>before<p>paragraph</p>after</div>`, []string{"before\nparagraph\nafter"}},
		{"literal-trailing", "<div data-sp-rte>literal\n\n</div>", []string{"literal\n\n"}},
		{"literal-break", `<div data-sp-rte>literal<br><br></div>`, []string{"literal\n\n"}},
	} {
		canvas, _ := json.Marshal(`<div data-sp-canvascontrol data-sp-controldata='{"controlType":4}'>` + test.inside + `</div>`)
		expression := `(async()=>{
			const location={origin:"https://fixture.sharepoint.com",hostname:"fixture.sharepoint.com"};
			` + fixturePrelude + `payloads[0].ListItemAllFields.CanvasContent1=` + string(canvas) + `;
			let index=0;
			const fetch=async url=>{const r=new Response(JSON.stringify(payloads[index++]),{headers:{"content-type":"application/json"}});Object.defineProperty(r,"url",{value:String(url)});return r;};
			return await (` + captureJS + `)(args,(` + networkJS + `));
		})()`
		var raw scriptResult
		if err := page.Eval(ctx, expression, &raw); err != nil {
			t.Fatal("synthetic RTE regression evaluation failed")
		}
		if len(raw.PageControls) != 1 || !reflect.DeepEqual(raw.PageControls[0].Texts, test.want) {
			t.Errorf("%s: %#v want %q", test.name, raw.PageControls, test.want)
		}
	}
}

func TestReviewIndependentStateAndObservationCaps(t *testing.T) {
	for _, test := range []struct {
		kind, state string
		status      int
	}{
		{"page", "no_transcript", 200}, {"page", "transcript_selection_required", 200}, {"page", "collection_incomplete", 200},
		{"stream", "no_access", 200}, {"stream", "not_found", 403}, {"stream", "timeout", -1}, {"stream", "transcript_observations", 200},
	} {
		raw := nativeResult()
		raw.State, raw.HTTPStatus = test.state, test.status
		if test.state == "transcript_observations" {
			raw.Transcript = &Transcript{ID: "transcript", Entries: []TranscriptEntry{{ID: "entry", Text: "text"}}}
			raw.File.DriveID = nil
			raw.File.ItemID = nil
		}
		got, err := decode(context.Background(), admittedRequest{Kind: test.kind, Host: raw.Account.Host, Path: raw.File.Path}, raw)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Errorf("contradictory %s/%s/%d published: %#v,%v", test.kind, test.state, test.status, got, err)
		}
	}
	raw := nativeResult()
	raw.State = "page_observations"
	kind := int64(4)
	raw.PageControls = []PageControl{{Type: &kind, State: "text", Texts: make([]string, 65537)}}
	if got, err := decode(context.Background(), admittedRequest{Kind: "page", Host: raw.Account.Host, Path: raw.File.Path}, raw); err == nil || !reflect.DeepEqual(got, Result{}) {
		t.Error("zero-byte observation count exceeded native work envelope")
	}
}

func TestReviewIgnoredCanvasCandidateBudget(t *testing.T) {
	ctx, page := scriptPage(t)
	for _, extra := range []int{0, 1} {
		count := 4096 + extra
		setup := `payloads[0].ListItemAllFields.CanvasContent1='<template>'+'<div data-sp-canvascontrol data-sp-controldata=\'{"controlType":0}\'></div>'.repeat(` + strings.TrimSpace(string(mustJSON(t, count))) + `)+"</template>";`
		want := "page_observations"
		if extra == 1 {
			want = "too_large"
		}
		if got := runCaptureFixture(t, ctx, page, fixturePrelude+setup); got != want {
			t.Errorf("ignored %d candidate controls: %s want %s", count, got, want)
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
