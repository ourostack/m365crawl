package engagecontent_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/engagecontent"
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
	profile := filepath.Join(t.TempDir(), "archive", "profile")
	t.Logf("owned synthetic profile (normal test cleanup): %s", profile)
	b, err := browser.Launch(ctx, browser.LaunchOptions{Exe: exe, Kind: kind, Profile: profile, Headless: true, DisposableFixture: true})
	if err != nil {
		t.Fatal(err)
	}
	var owned map[int]fixtureProcess
	t.Cleanup(func() {
		closed := true
		if err := b.Close(); err != nil {
			t.Error(err)
			closed = false
		}
		if runtime.GOOS == "darwin" {
			after := fixtureProcessCensus(t)
			for pid, p := range after {
				original, known := owned[pid]
				if strings.Contains(p.argv, profile) || (known && p.born == original.born) {
					closed = false
					t.Errorf("owned synthetic generation remains: pid=%d birth=%s profile=%s", pid, p.born, profile)
				}
			}
			t.Logf("synthetic generation closure verified=%t profile=%s generations=%d", closed, profile, len(owned))
		}
	})
	p, err := b.Page(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		all := fixtureProcessCensus(t)
		owned = map[int]fixtureProcess{}
		root := 0
		for pid, p := range all {
			if strings.Contains(p.argv, "--user-data-dir="+profile) && !strings.Contains(p.argv, "--type=") {
				root = pid
				if !strings.Contains(p.argv, "--headless") || !strings.Contains(p.argv, "--use-mock-keychain") {
					t.Fatal("actual disposable launch flags missing")
				}
			}
			if strings.Contains(p.argv, profile) {
				owned[pid] = p
			}
		}
		if root == 0 {
			t.Fatal("owned synthetic root generation not found")
		}
		for changed := true; changed; {
			changed = false
			for pid, p := range all {
				if _, known := owned[pid]; known {
					continue
				}
				if _, parent := owned[p.ppid]; parent {
					owned[pid] = p
					changed = true
				}
			}
		}
		for pid, p := range owned {
			t.Logf("owned synthetic process generation pid=%d birth=%s profile=%s root=%d root_flags_verified=headless,mock-keychain", pid, p.born, profile, root)
		}
	}
	return ctx, p
}

type fixtureProcess struct {
	ppid       int
	born, argv string
}

func fixtureProcessCensus(t *testing.T) map[int]fixtureProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,ppid=,lstart=,command=").Output()
	if err != nil {
		t.Fatal("synthetic generation census failed:", err)
	}
	out := map[int]fixtureProcess{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		pid, e1 := strconv.Atoi(fields[0])
		ppid, e2 := strconv.Atoi(fields[1])
		if e1 != nil || e2 != nil {
			t.Fatal("invalid process generation census")
		}
		out[pid] = fixtureProcess{ppid, strings.Join(fields[2:7], " "), strings.Join(fields[7:], " ")}
	}
	return out
}

func captureSources(t *testing.T) string {
	t.Helper()
	return engagecontent.InitScriptForTest
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
