package engagecontent

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/browser"
)

func TestReviewViewerFragmentsCountedOnce(t *testing.T) {
	var raw scriptResult
	runCapture(t, nativeFixtureJS+`
		viewer.viewer.user.id="v".repeat(4096);
		for(let i=0;i<64;i++) await send(viewer);
		for(let i=0;i<64;i++) await send({viewer:{user:{id:viewer.viewer.user.id}}});
		return await window.__m365crawlEngageCapture.stop();
	`, &raw)
	if raw.Fatal != "" {
		t.Fatalf("script refused under-budget evidence: %s", raw.Fatal)
	}
	if _, err := decode(context.Background(), raw); err != nil {
		t.Fatalf("repeated fragments charged more than once: %v", err)
	}
}

func TestReviewWireVersionPreservesNumericEvidence(t *testing.T) {
	for _, tc := range []struct {
		token string
		known bool
	}{
		{"1.0000000000000001", false},
		{"9007199254740990.5", false},
		{"9007199254740991", true},
		{"2", true},
		{"2.0", true},
	} {
		t.Run(tc.token, func(t *testing.T) {
			var raw scriptResult
			token, err := json.Marshal(tc.token)
			if err != nil {
				t.Fatal(err)
			}
			runCapture(t, nativeFixtureJS+`
				await send(viewer);
				const wire=JSON.stringify({data:{thread}}).replace('"version":2','"version":'+`+string(token)+`);
				replies.push({fixtureResponse:new Response(wire,{headers:{"content-type":"application/json"}})});
				await window.fetch("https://engage.cloud.microsoft/graphql");
				await new Promise(r=>setTimeout(r,30));
				return await window.__m365crawlEngageCapture.stop();
			`, &raw)
			if raw.Fatal != "" || len(raw.Threads) != 1 {
				t.Fatalf("native wire admission: %+v", raw)
			}
			got, err := decode(context.Background(), raw)
			if err != nil || (got.Threads[0].Version != nil) != tc.known {
				t.Fatalf("wire token%s precision changed: %+v, %v", tc.token, got, err)
			}
		})
	}
}

const syntheticLocation = `const location={protocol:"https:",hostname:"engage.cloud.microsoft",port:"",origin:"https://engage.cloud.microsoft"};`

type documentDriver struct{ page *browser.Page }

func (d documentDriver) AddInitScript(ctx context.Context, source string) (string, error) {
	return d.page.AddInitScript(ctx, `(() => {`+syntheticLocation+`
		window.fetch=async()=>new Response(JSON.stringify({data:{
			viewer:{user:{id:"viewer",network:{id:"network"}}},
			thread:{__typename:"Thread",id:"thread",network:{id:"network"},group:{id:"group"},
			threadStarter:{id:"starter",languageSpecificContent:{body:{serializedContentState:'{"blocks":[{"text":"synthetic"}]}'}}}}
		}}),{headers:{"content-type":"application/json"}});
		`+source+`})();`)
}

func (d documentDriver) RemoveInitScript(ctx context.Context, id string) error {
	return d.page.RemoveInitScript(ctx, id)
}

func (d documentDriver) Navigate(ctx context.Context, _ string) error {
	return d.replace(ctx, "initial")
}

func (d documentDriver) replace(ctx context.Context, name string) error {
	if err := d.page.Navigate(ctx, "data:text/html,"+url.PathEscape(`<html><body><button>Home</button><div>`+name+`</div></body></html>`)); err != nil {
		return err
	}
	return d.page.Eval(ctx, `(async()=>{await window.fetch("https://engage.cloud.microsoft/graphql");await new Promise(r=>setTimeout(r,30));})()`, nil)
}

func (d documentDriver) Host(context.Context) (string, error) { return "engage.cloud.microsoft", nil }

func (d documentDriver) Eval(ctx context.Context, expr string, out any) error {
	if strings.Contains(expr, "location.") {
		expr = `(()=>{` + syntheticLocation + `return ` + expr + `;})()`
	}
	return d.page.Eval(ctx, expr, out)
}

func TestReviewDocumentReplacementInvalidatesCapture(t *testing.T) {
	ctx, page := nativeScriptPage(t)
	driver := documentDriver{page: page}
	previous := waitCollection
	waitCollection = func(ctx context.Context) error { return driver.replace(ctx, "replacement") }
	t.Cleanup(func() { waitCollection = previous })
	if got, err := ObserveHome(ctx, driver); err == nil {
		t.Fatalf("replaced document published without its native Home action: %+v", got)
	}
}

func TestReviewInvisibleHomeIsNotNativeAction(t *testing.T) {
	ctx, page := nativeScriptPage(t)
	for _, style := range []string{"visibility:hidden", "opacity:0", "display:none"} {
		raw, err := json.Marshal(`<button style="` + style + `" onclick="window.clicked=true">Home</button>`)
		if err != nil {
			t.Fatal(err)
		}
		if err := page.Eval(ctx, `document.body.innerHTML=`+string(raw), nil); err != nil {
			t.Fatal(err)
		}
		var state struct{ Home bool }
		if err := page.Eval(ctx, `(()=>{`+syntheticLocation+`return `+homeStateExpr+`;})()`, &state); err != nil {
			t.Fatal(err)
		}
		var clicked json.RawMessage
		if err := page.Eval(ctx, `(()=>{`+syntheticLocation+`return `+homeClickExpr+`;})()`, &clicked); err != nil {
			t.Fatal(err)
		}
		var plain bool
		var action struct{ Clicked bool }
		_ = json.Unmarshal(clicked, &plain)
		_ = json.Unmarshal(clicked, &action)
		if state.Home || plain || action.Clicked {
			t.Fatalf("hidden Home claimed visible action for %s", style)
		}
	}
}

func TestReviewIndependentOptionalScalarEvidence(t *testing.T) {
	raw := nativeResult()
	raw.Threads[0].Title = json.RawMessage(`"\ud800"`)
	if got, err := decode(context.Background(), raw); err == nil {
		t.Fatalf("malformed raw optional Unicode normalized: %+v", got)
	}
	for _, scalar := range []json.RawMessage{[]byte(" null "), []byte("\nnull\t")} {
		raw = nativeResult()
		raw.Threads[0].IsDeleted = scalar
		got, err := decode(context.Background(), raw)
		if err != nil || got.Threads[0].IsDeleted != nil {
			t.Fatalf("JSON null turned into known false: %+v, %v", got, err)
		}
	}
	for _, scalar := range []json.RawMessage{
		[]byte(`"\udc00"`), []byte(`"\ud800\u0041"`), []byte(`"` + "\xff" + `"`),
	} {
		raw = nativeResult()
		raw.Threads[0].Title = scalar
		if got, err := decode(context.Background(), raw); err == nil {
			t.Fatalf("malformed optional scalar published: %+v", got)
		}
	}
	for _, scalar := range []json.RawMessage{
		[]byte(`"\ud83d\ude00"`), []byte(`"\\ud800"`), []byte(`"\u0061\n"`), []byte(`"literal"`),
	} {
		raw = nativeResult()
		raw.Threads[0].Title = scalar
		if got, err := decode(context.Background(), raw); err != nil || got.Threads[0].Title == nil {
			t.Fatalf("valid exact string scalar refused: %+v, %v", got, err)
		}
	}
}

func TestReviewRegistrationGenerationAndVersionEnvelopes(t *testing.T) {
	quickCollection(t)
	for _, generation := range []string{"", strings.Repeat("x", 129)} {
		raw := nativeResult()
		raw.Generation = generation
		if got, err := ObserveHome(context.Background(), &fakeDriver{raw: raw}); err == nil {
			t.Fatalf("missing/oversized document generation published: %+v", got)
		}
	}
	if got, err := ObserveHome(context.Background(), &fakeDriver{raw: nativeResult(), clickGeneration: "replacement"}); err == nil {
		t.Fatalf("document replaced before click published: %+v", got)
	}
	for _, value := range []json.RawMessage{
		[]byte(`{"nativeNumber":`), rawValue(map[string]any{"nativeNumber": 2}),
		rawValue(map[string]any{"nativeNumber": strings.Repeat("1", 257)}),
	} {
		raw := nativeResult()
		raw.Threads[0].Version = value
		if got, err := decode(context.Background(), raw); err == nil {
			t.Fatalf("malformed native number envelope published: %+v", got)
		}
	}
}
