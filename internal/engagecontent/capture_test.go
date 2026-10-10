package engagecontent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
)

func nativeScriptPage(t *testing.T) (context.Context, *browser.Page) {
	t.Helper()
	exe, kind, err := browser.Find("edge")
	if err != nil {
		if os.Getenv("M365CRAWL_REQUIRE_BROWSER") == "1" {
			t.Fatal(err)
		}
		t.Skip("synthetic native capture requires Edge")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	b, err := browser.Launch(ctx, browser.LaunchOptions{Exe: exe, Kind: kind, Profile: filepath.Join(t.TempDir(), "archive", "profile"), Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	p, err := b.Page(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, p
}

func captureSources(t *testing.T) string {
	t.Helper()
	return initScript
}

func runCapture(t *testing.T, body string, out any) {
	t.Helper()
	ctx, p := nativeScriptPage(t)
	expr := `(async () => {
		const location = {protocol:"https:",hostname:"engage.cloud.microsoft",port:"",origin:"https://engage.cloud.microsoft"};
		const original = window.fetch;
		const replies = [];
		const fixtureResponses = [];
		const fixtureClones = new Set();
		const trackResponse = response => {
			fixtureResponses.push(response);
			const clone=response.clone.bind(response);
			response.clone=()=>{
				const copy=clone();
				const getReader=copy.body.getReader.bind(copy.body);
				copy.body.getReader=(...args)=>{
					const reader=getReader(...args);
					let released;
					const completion=new Promise(resolve=>{released=resolve;});
					fixtureClones.add(completion);
					const release=reader.releaseLock.bind(reader);
					reader.releaseLock=()=>{
						try {return release();}
						finally {fixtureClones.delete(completion);released();}
					};
					return reader;
				};
				return copy;
			};
			return response;
		};
		const captureIdle=async()=>{
			await Promise.all(fixtureResponses.filter(response=>!response.bodyUsed).map(response=>response.text()));
			let timer;
			try {
				await Promise.race([
					Promise.all([...fixtureClones]),
					new Promise((_,reject)=>{timer=setTimeout(()=>reject(new Error("synthetic clone did not release")),5000);})
				]);
			} finally {clearTimeout(timer);}
		};
		window.fetch = async () => {
			const reply = replies.shift();
			return trackResponse(reply?.fixtureResponse || new Response(JSON.stringify(reply), {status:200,headers:{"content-type":"application/json"}}));
		};
		try {` + captureSources(t) + body + `}
		finally { window.fetch = original; delete window.__m365crawlEngageCapture; }
	})()`
	if err := p.Eval(ctx, expr, out); err != nil {
		t.Fatal(err)
	}
}

const nativeFixtureJS = `
	const viewer = {viewer:{user:{id:"viewer",network:{id:"network"}}}};
	const thread = {
		__typename:"Thread",id:"thread",network:{id:"network"},group:{id:"group"},
		createdAt:"2026-10-10T00:00:00Z",updatedAt:"2026-10-10T01:00:00Z",
		threadStarter:{id:"starter",sender:{id:"sender"},version:2,isDeleted:false,isDraft:false,
			createdAt:"2026-10-10T00:00:00Z",updatedAt:"2026-10-10T01:00:00Z",
			languageSpecificContent:{language:"en",title:"title",body:{
				serializedContentState:JSON.stringify({blocks:[{text:"a"},{text:""},{text:"b\n"}],entityMap:{}}),references:[]}}},
		topLevelReplies:{totalCount:0},hasAttachments:false
	};
	const send = async data => {
		replies.push({data});
		const response = await window.fetch("https://engage.cloud.microsoft/graphql", {method:"POST"});
		if (!(await response.json()).data) throw new Error("original response consumed");
		await captureIdle();
	};
`

func TestCaptureNativeViewerAndThreadProjection(t *testing.T) {
	var got struct {
		State, Fatal string
		Account      struct{ Host, NetworkID, UserID string }
		Threads      []struct {
			ID, StarterID string
			Blocks        []string
			Title         *string
		}
	}
	runCapture(t, nativeFixtureJS+`
		await send({initial:thread});
		await send(viewer);
		await send({afterNativeAction:thread});
		return await window.__m365crawlEngageCapture.stop();
	`, &got)
	if got.Fatal != "" || got.State != "observations" || got.Account.UserID != "viewer" || got.Account.NetworkID != "network" || len(got.Threads) != 2 {
		t.Fatalf("capture admission: %+v", got)
	}
	for _, thread := range got.Threads {
		if thread.ID != "thread" || thread.StarterID != "starter" || thread.Title == nil || *thread.Title != "title" ||
			len(thread.Blocks) != 3 || thread.Blocks[0] != "a" || thread.Blocks[1] != "" || thread.Blocks[2] != "b\n" {
			t.Fatalf("literal native projection: %+v", thread)
		}
	}
}

func TestCaptureViewerIdentityAndFragments(t *testing.T) {
	for _, tc := range []struct{ name, body, fatal, state string }{
		{"directory-not-viewer", `await send({directory:{id:"viewer",userPrincipalName:"synthetic"},thread});`, "identity_missing", ""},
		{"viewer-drift", `await send(viewer); await send({viewer:{user:{id:"other",network:{id:"network"}}},thread});`, "identity_drift", ""},
		{"network-drift", `await send(viewer); await send({viewer:{user:{id:"viewer",network:{id:"other"}}},thread});`, "identity_drift", ""},
		{"fragment", `await send(viewer); await send({fragment:{__typename:"Thread",id:"thread"}});`, "", "metadata_only"},
		{"foreign-network", `thread.network.id="foreign"; await send(viewer); await send({thread});`, "", "metadata_only"},
		{"partial-viewer-after-full", `await send(viewer); await send({viewer:{user:{id:"viewer",wallFeed:{}}}});`, "", "metadata_only"},
		{"partial-viewer-before-full", `await send({viewer:{user:{id:"viewer",wallFeed:{}}}}); await send(viewer);`, "", "metadata_only"},
		{"conflicting-partial-viewer", `await send({viewer:{user:{id:"other",wallFeed:{}}}}); await send(viewer);`, "identity_drift", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got struct {
				State, Fatal string
				Threads      []json.RawMessage
				Losses       []struct {
					Code  string
					Count int
				}
			}
			runCapture(t, nativeFixtureJS+tc.body+`return await window.__m365crawlEngageCapture.stop();`, &got)
			if got.Fatal != tc.fatal || got.State != tc.state || len(got.Threads) != 0 {
				t.Fatalf("identity/state: %+v", got)
			}
			if tc.fatal == "" && len(got.Losses) == 0 {
				t.Fatal("omitted fragment/network was silently empty")
			}
		})
	}
}

func TestCaptureMalformedDraftAndGraphQLErrors(t *testing.T) {
	for _, draft := range []string{`"not json"`, `JSON.stringify({blocks:null})`, `JSON.stringify({blocks:[{text:7}]})`} {
		var got struct{ Fatal string }
		runCapture(t, nativeFixtureJS+`thread.threadStarter.languageSpecificContent.body.serializedContentState=`+draft+`;
			await send(viewer); await send({thread}); return await window.__m365crawlEngageCapture.stop();`, &got)
		if got.Fatal != "malformed" {
			t.Fatalf("malformed Draft admitted: %+v", got)
		}
	}
	var got struct {
		Fatal  string
		Losses []struct{ Code string }
	}
	runCapture(t, nativeFixtureJS+`
		replies.push({data:viewer,errors:[{message:"synthetic private error"}]});
		await window.fetch("https://engage.cloud.microsoft/graphql", {method:"POST"});
		await captureIdle();
		return await window.__m365crawlEngageCapture.stop();
	`, &got)
	if got.Fatal != "" || len(got.Losses) == 0 || got.Losses[0].Code != "graphql_error" {
		t.Fatalf("GraphQL error disappeared: %+v", got)
	}
}

func TestCaptureWrapperOwnershipAndStoppedRequests(t *testing.T) {
	var got struct{ Preserved, NoLateAppend bool }
	runCapture(t, nativeFixtureJS+`
		await send(viewer); await send({thread});
		const ours=window.fetch;
		const next=async (...args)=>ours(...args);
		window.fetch=next;
		const result=await window.__m365crawlEngageCapture.stop();
		await send({thread});
		return {Preserved:window.fetch===next,NoLateAppend:result.Threads.length===1};
	`, &got)
	if !got.Preserved || !got.NoLateAppend {
		t.Fatalf("wrapper/late ownership: %+v", got)
	}
}
