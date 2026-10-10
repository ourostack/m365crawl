package sharepointcontent

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNativePageControlProjection(t *testing.T) {
	ctx, page := scriptPage(t)
	canvas := `<div data-sp-canvascontrol data-sp-controldata='{"controlType":0}'></div>
<div data-sp-canvascontrol data-sp-controldata='{"controlType":3,"id":"dddddddd-dddd-dddd-dddd-dddddddddddd"}'><div data-sp-rte>omitted webpart</div></div>
<div data-sp-canvascontrol data-sp-controldata='{"controlType":4,"id":"eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"}'><div data-sp-rte><p>Hello &amp; world<br>Next</p><script>script text</script><style>style text</style><svg><text>svg text</text></svg><div data-sp-canvascontrol data-sp-controldata='{"controlType":4}'><div data-sp-rte>Inner</div></div></div></div>
<div data-sp-canvascontrol data-sp-controldata='broken'><div data-sp-rte>untyped text</div></div>`
	args, _ := json.Marshal(struct {
		admittedRequest
		Canvas string
	}{admittedRequest{Kind: "page", Host: "fixture.sharepoint.com", Site: "/sites/sample", Path: "/sites/sample/SitePages/Page.aspx"}, canvas})
	expression := `(async args=>{
		const location={origin:"https://fixture.sharepoint.com",hostname:"fixture.sharepoint.com"};
		const payloads=[
			{UniqueId:"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",ServerRelativeUrl:args.Path,ListItemAllFields:{CanvasContent1:args.Canvas}},
			{Id:"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
			{Id:"cccccccc-cccc-cccc-cccc-cccccccccccc"},
			{Id:11,LoginName:"fixture-account"}];
		let calls=0;
		const fetch=async url=>{
			const response=new Response(JSON.stringify(payloads[calls++]),{headers:{"content-type":"application/json"}});
			Object.defineProperty(response,"url",{value:String(url)});return response;
		};
		return await (` + captureJS + `)(args,(` + networkJS + `));
	})(` + string(args) + `)`
	var got scriptResult
	if err := page.Eval(ctx, expression, &got); err != nil {
		t.Fatal("synthetic page projection evaluation failed")
	}
	if got.State != "page_observations" || len(got.PageControls) != 5 {
		t.Fatalf("native controls not projected: %#v", got)
	}
	if got.PageControls[0].State != "layout" || got.PageControls[1].State != "omitted" || len(got.PageControls[1].Texts) != 0 ||
		!reflect.DeepEqual(got.PageControls[2].Texts, []string{"Hello & world\nNext"}) ||
		!reflect.DeepEqual(got.PageControls[3].Texts, []string{"Inner"}) || got.PageControls[4].State != "unmapped" {
		t.Fatalf("nested/executable/webpart text leaked: %#v", got.PageControls)
	}
}
