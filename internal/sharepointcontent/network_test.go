package sharepointcontent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
)

func scriptPage(t *testing.T) (context.Context, *browser.Page) {
	t.Helper()
	exe, kind, err := browser.Find("edge")
	if err != nil {
		if os.Getenv("M365CRAWL_REQUIRE_BROWSER") == "1" {
			t.Fatal("required synthetic script browser unavailable")
		}
		t.Skip("synthetic script test requires Edge")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	profile := filepath.Join(t.TempDir(), "archive", "profile")
	b, err := browser.Launch(ctx, browser.LaunchOptions{Exe: exe, Kind: kind, Profile: profile, Headless: true, StartURL: "about:blank"})
	if err != nil {
		t.Fatalf("synthetic script browser launch failed: %v", err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Error("synthetic script browser cleanup failed")
		}
	})
	page, err := b.Page(ctx)
	if err != nil {
		t.Fatal("synthetic script page unavailable")
	}
	return ctx, page
}

func TestNativeJSONReaderResponseStates(t *testing.T) {
	ctx, page := scriptPage(t)
	expression := `(async () => {
		const replies = [
			{status:200,type:"application/json",body:'{"fixture":true}'},
			{status:403,type:"application/json",body:'{}'},
			{status:404,type:"application/json",body:'{}'},
			{status:401,type:"application/json",body:'{}'},
			{status:503,type:"application/json",body:'{}'},
			{status:200,type:"text/plain",body:'{}'},
			{status:200,type:"application/json",body:'{'},
			{status:200,type:"application/json",body:'{}',url:"https://other.sharepoint.com/result"}
		];
		let calls = 0, cancelled = 0;
		const methods = [], credentials = [];
		const fetch = async (url, options) => {
			methods.push(options.method);credentials.push(options.credentials);
			const item = replies[calls++];
			const body = new ReadableStream({
				start(controller) {controller.enqueue(new TextEncoder().encode(item.body));controller.close();},
				cancel() {cancelled++;}
			});
			const response = new Response(body,{status:item.status,headers:{"content-type":item.type}});
			Object.defineProperty(response,"url",{value:item.url || String(url)});
			return response;
		};
		const controller = new AbortController();
		const read = (` + networkJS + `)({
			origin:"https://fixture.sharepoint.com",signal:controller.signal,
			maxBodyBytes:2097152,maxTotalBytes:8388608,maxRequests:8
		});
		const states = [];
		for (let i=0;i<replies.length;i++) states.push((await read("/native")).state);
		return {states,calls,cancelled,methods,credentials};
	})()`
	var got struct {
		States, Methods, Credentials []string
		Calls, Cancelled             int
	}
	if err := page.Eval(ctx, expression, &got); err != nil {
		t.Fatal("synthetic reader evaluation failed")
	}
	want := []string{"ok", "no_access", "not_found", "signin_required", "failed", "unexpected_response", "malformed", "elsewhere"}
	if !reflect.DeepEqual(got.States, want) || got.Calls != 8 || got.Cancelled != 6 {
		t.Fatalf("native reader states/body closure: %#v", got)
	}
	for i := range got.Methods {
		if got.Methods[i] != "GET" || got.Credentials[i] != "same-origin" {
			t.Fatalf("unexpected method/credential mode: %#v", got)
		}
	}
}

func TestNativeJSONReaderActualByteAndRequestBounds(t *testing.T) {
	ctx, page := scriptPage(t)
	args, err := json.Marshal(map[string]int{"body": 2 << 20, "total": 8 << 20, "requests": 8})
	if err != nil {
		t.Fatal(err)
	}
	expression := `(async (limits) => {
		const controller = new AbortController();
		let calls=0,cancelled=0;
		let fetch = async url => {
			calls++;
			const text = calls<=4 ? "{}"+" ".repeat(limits.body-2) : "0";
			const response = new Response(new ReadableStream({
				start(c) {c.enqueue(new TextEncoder().encode(text));c.close();},
				cancel() {cancelled++;}
			}),{headers:{"content-type":"application/json"}});
			Object.defineProperty(response,"url",{value:String(url)});
			return response;
		};
		const factory = (` + networkJS + `);
		const read = factory({origin:"https://fixture.sharepoint.com",signal:controller.signal,maxBodyBytes:limits.body,maxTotalBytes:limits.total,maxRequests:limits.requests});
		const states=[];
		for(let i=0;i<5;i++) states.push((await read("/native")).state);
		calls=0;
		fetch = async url => {
			calls++;
			const response = new Response("{}",{headers:{"content-type":"application/json"}});
			Object.defineProperty(response,"url",{value:String(url)});
			return response;
		};
		const requestRead=factory({origin:"https://fixture.sharepoint.com",signal:controller.signal,maxBodyBytes:limits.body,maxTotalBytes:limits.total,maxRequests:limits.requests});
		const requestStates=[];
		for(let i=0;i<9;i++) requestStates.push((await requestRead("/native")).state);
		const requestCalls=calls;
		fetch = async url => {
			const response = new Response("{}"+" ".repeat(limits.body-1),{headers:{"content-type":"application/json"}});
			Object.defineProperty(response,"url",{value:String(url)});
			return response;
		};
		const bodyRead=factory({origin:"https://fixture.sharepoint.com",signal:controller.signal,maxBodyBytes:limits.body,maxTotalBytes:limits.total,maxRequests:limits.requests});
		const bodyOverflow=(await bodyRead("/native")).state;
		return {states,calls:requestCalls,cancelled,requestStates,bodyOverflow};
	})(` + string(args) + `)`
	var got struct {
		States, RequestStates []string
		Calls, Cancelled      int
		BodyOverflow          string
	}
	if err := page.Eval(ctx, expression, &got); err != nil {
		t.Fatal("synthetic bound evaluation failed")
	}
	if !reflect.DeepEqual(got.States, []string{"ok", "ok", "ok", "ok", "too_large"}) {
		t.Fatalf("actual total-byte boundary not enforced: %#v", got)
	}
	if !reflect.DeepEqual(got.RequestStates, []string{"ok", "ok", "ok", "ok", "ok", "ok", "ok", "ok", "too_large"}) || got.Calls != 8 || got.BodyOverflow != "too_large" {
		t.Fatalf("actual request-count boundary not enforced: %#v", got)
	}
}

func TestNativeJSONReaderAbortAndCredentialRedirect(t *testing.T) {
	ctx, page := scriptPage(t)
	expression := `(async () => {
		let calls=0,final="https://fixture.sharepoint.com/result",cancelled=0;
		let during=null;
		const fetch=async url => {
			calls++;
			if(during) during.abort();
			const response=new Response(new ReadableStream({
				start(c) {c.enqueue(new TextEncoder().encode("{}"));c.close();},
				cancel() {cancelled++;}
			}),{headers:{"content-type":"application/json"}});
			Object.defineProperty(response,"url",{value:final});
			return response;
		};
		const factory=(` + networkJS + `);
		const make=c => factory({origin:"https://fixture.sharepoint.com",signal:c.signal,maxBodyBytes:2097152,maxTotalBytes:8388608,maxRequests:8});
		const first=new AbortController();first.abort();
		const before=(await make(first)("/native")).state;
		const beforeCalls=calls;
		during=new AbortController();
		const afterFetch=(await make(during)("/native")).state;
		during=null;
		final="https://user:pass@fixture.sharepoint.com/result";
		const redirected=(await make(new AbortController())("/native")).state;
		return {before,beforeCalls,afterFetch,redirected,cancelled};
	})()`
	var got struct {
		Before, AfterFetch, Redirected string
		BeforeCalls, Cancelled         int
	}
	if err := page.Eval(ctx, expression, &got); err != nil {
		t.Fatal("synthetic abort/redirect evaluation failed")
	}
	if got.Before != "timeout" || got.BeforeCalls != 0 || got.AfterFetch != "timeout" || got.Redirected != "elsewhere" || got.Cancelled != 2 {
		t.Fatalf("abandoned/credential-bearing response admitted: %#v", got)
	}
}
