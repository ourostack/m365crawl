package sharepointcontent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
)

func runCaptureFixture(t *testing.T, ctx context.Context, page *browser.Page, body string) string {
	t.Helper()
	expression := `(async () => {
		const location={origin:"https://fixture.sharepoint.com",hostname:"fixture.sharepoint.com"};
		` + body + `
		let index=0;
		const fetch=async url=>{
			const response=new Response(JSON.stringify(payloads[index++]),{headers:{"content-type":"application/json"}});
			Object.defineProperty(response,"url",{value:String(url)});return response;
		};
		return (await (` + captureJS + `)(args,(` + networkJS + `))).state;
	})()`
	var state string
	if err := page.Eval(ctx, expression, &state); err != nil {
		t.Fatal("synthetic capture boundary evaluation failed")
	}
	return state
}

const fixturePrelude = `
const args={Kind:"page",Host:"fixture.sharepoint.com",Site:"/sites/sample",Path:"/sites/sample/SitePages/Page.aspx"};
const payloads=[
{UniqueId:"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",ServerRelativeUrl:args.Path,VroomDriveID:"drive",VroomItemID:"item",ListItemAllFields:{}},
{Id:"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
{Id:"cccccccc-cccc-cccc-cccc-cccccccccccc"},
{Id:11,LoginName:"fixture-account"}];`

func TestNativePageRuntimeWorkAndTextCaps(t *testing.T) {
	ctx, page := scriptPage(t)
	for _, test := range []struct {
		name, setup, want string
	}{
		{"controls-inclusive", `payloads[0].ListItemAllFields.CanvasContent1='<div data-sp-canvascontrol data-sp-controldata=\'{"controlType":0}\'></div>'.repeat(4096);`, "page_observations"},
		{"controls-over", `payloads[0].ListItemAllFields.CanvasContent1='<div data-sp-canvascontrol data-sp-controldata=\'{"controlType":0}\'></div>'.repeat(4097);`, "too_large"},
		{"nodes-inclusive", `payloads[0].ListItemAllFields.CanvasContent1='<i></i>'.repeat(65535);`, "page_observations"},
		{"nodes-over", `payloads[0].ListItemAllFields.CanvasContent1='<i></i>'.repeat(65536);`, "too_large"},
		{"ignored-template-nodes-over", `payloads[0].ListItemAllFields.CanvasContent1='<template>'+'<i></i>'.repeat(65536)+'</template>';`, "too_large"},
		{"depth-inclusive", `payloads[0].ListItemAllFields.CanvasContent1='<div>'.repeat(128)+'</div>'.repeat(128);`, "page_observations"},
		{"depth-over", `payloads[0].ListItemAllFields.CanvasContent1='<div>'.repeat(129)+'</div>'.repeat(129);`, "too_large"},
		{"text-inclusive", `payloads[0].ListItemAllFields.CanvasContent1=('<div data-sp-canvascontrol data-sp-controldata=\'{"controlType":4}\'><div data-sp-rte>'+'x'.repeat(262144)+'</div></div>').repeat(4);`, "page_observations"},
		{"text-over", `payloads[0].ListItemAllFields.CanvasContent1=('<div data-sp-canvascontrol data-sp-controldata=\'{"controlType":4}\'><div data-sp-rte>'+'x'.repeat(262144)+'</div></div>').repeat(4)+'<div data-sp-canvascontrol data-sp-controldata=\'{"controlType":4}\'><div data-sp-rte>x</div></div>';`, "too_large"},
		{"field-over", `payloads[0].ListItemAllFields.CanvasContent1='<div data-sp-canvascontrol data-sp-controldata=\'{"controlType":4}\'><div data-sp-rte>'+'x'.repeat(262145)+'</div></div>';`, "too_large"},
		{"unpaired-native-brace", `payloads[0].UniqueId="{"+payloads[0].UniqueId;`, "malformed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := runCaptureFixture(t, ctx, page, fixturePrelude+test.setup); got != test.want {
				t.Fatalf("%s runtime cap: got %s want %s", test.name, got, test.want)
			}
		})
	}
}

func TestNativeTranscriptRuntimeCaps(t *testing.T) {
	ctx, page := scriptPage(t)
	base := fixturePrelude + `
args.Kind="stream";
payloads.push({value:[{id:"transcript",temporaryDownloadUrl:"https://fixture.sharepoint.com/download"}]});
payloads.push({entries:[{id:"entry",text:"text"}]});`
	for _, test := range []struct{ name, setup, want string }{
		{"entries-inclusive", `payloads[5].entries=Array.from({length:16384},()=>({id:"entry",text:""}));`, "transcript_observations"},
		{"entries-over", `payloads[5].entries=Array.from({length:16385},()=>({id:"entry",text:""}));`, "too_large"},
		{"text-inclusive", `payloads[5].entries=Array.from({length:4},()=>({id:"entry",text:"x".repeat(262144)}));`, "transcript_observations"},
		{"text-over", `payloads[5].entries=Array.from({length:4},()=>({id:"entry",text:"x".repeat(262144)}));payloads[5].entries.push({id:"entry",text:"x"});`, "too_large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := runCaptureFixture(t, ctx, page, base+test.setup); got != test.want {
				t.Fatalf("%s: %s want %s", test.name, got, test.want)
			}
		})
	}
	for _, field := range []string{"text", "id", "speakerDisplayName", "startOffset", "endOffset"} {
		key, _ := json.Marshal(field)
		if got := runCaptureFixture(t, ctx, page, base+`payloads[5].entries[0][`+string(key)+`]="x".repeat(262145);`); got != "too_large" {
			t.Errorf("%s per-string overrun: got %s want too_large", field, got)
		}
	}
}
