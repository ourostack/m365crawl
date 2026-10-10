package engagecontent

import "testing"

func TestCaptureResponseByteAndCountLimits(t *testing.T) {
	for _, tc := range []struct{ name, setup, fatal string }{
		{"body-exact", `const payload={data:viewer,padding:""};payload.padding="x".repeat(2097152-JSON.stringify(payload).length);replies.push(payload);await window.fetch("https://engage.cloud.microsoft/graphql");await new Promise(r=>setTimeout(r,30));`, ""},
		{"body-plus-one", `const payload={data:viewer,padding:""};payload.padding="x".repeat(2097153-JSON.stringify(payload).length);replies.push(payload);await window.fetch("https://engage.cloud.microsoft/graphql");await new Promise(r=>setTimeout(r,30));`, "too_large"},
		{"total-exact", `for(let i=0;i<4;i++){const payload={data:viewer,padding:""};payload.padding="x".repeat(2097152-JSON.stringify(payload).length);replies.push(payload);await window.fetch("https://engage.cloud.microsoft/graphql");await new Promise(r=>setTimeout(r,30));}`, ""},
		{"total-plus-one", `for(let i=0;i<4;i++){const payload={data:viewer,padding:""};payload.padding="x".repeat(2097152-JSON.stringify(payload).length);replies.push(payload);await window.fetch("https://engage.cloud.microsoft/graphql");await new Promise(r=>setTimeout(r,30));}await send(viewer);`, "too_large"},
		{"responses-exact", `for(let i=0;i<128;i++)await send(viewer);`, ""},
		{"responses-plus-one", `for(let i=0;i<129;i++)await send(viewer);`, "too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got struct{ Fatal string }
			runCapture(t, nativeFixtureJS+tc.setup+`return await window.__m365crawlEngageCapture.stop();`, &got)
			if got.Fatal != tc.fatal {
				t.Fatalf("response bound fatal=%q, want %q", got.Fatal, tc.fatal)
			}
		})
	}
}

func TestCaptureResponseFaultsAndUnqualifiedRequests(t *testing.T) {
	for _, tc := range []struct{ name, setup, fatal string }{
		{"nonjson", `replies.push({fixtureResponse:new Response("synthetic",{headers:{"content-type":"text/plain"}})});await window.fetch("https://engage.cloud.microsoft/graphql");`, "response_failed"},
		{"denied", `replies.push({fixtureResponse:new Response("{}",{status:403,headers:{"content-type":"application/json"}})});await window.fetch("https://engage.cloud.microsoft/graphql");`, "response_failed"},
		{"invalid-json", `replies.push({fixtureResponse:new Response("{",{headers:{"content-type":"application/json"}})});await window.fetch("https://engage.cloud.microsoft/graphql");`, "malformed"},
		{"redirect", `const response=new Response("{}",{headers:{"content-type":"application/json"}});Object.defineProperty(response,"url",{value:"https://unqualified.invalid/graphql"});replies.push({fixtureResponse:response});await window.fetch("https://engage.cloud.microsoft/graphql");`, "response_failed"},
		{"foreign-host-not-copied", `replies.push({fixtureResponse:new Response("{",{headers:{"content-type":"application/json"}})});await window.fetch("https://web.yammer.com/graphql");`, ""},
		{"credential-not-copied", `replies.push({fixtureResponse:new Response("{",{headers:{"content-type":"application/json"}})});await window.fetch("https://synthetic@engage.cloud.microsoft/graphql");`, ""},
		{"wrong-method-not-copied", `replies.push({fixtureResponse:new Response("{",{headers:{"content-type":"application/json"}})});await window.fetch("https://engage.cloud.microsoft/graphql",{method:"DELETE"});`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got struct{ Fatal string }
			runCapture(t, nativeFixtureJS+`await send(viewer);`+tc.setup+`
				await new Promise(r=>setTimeout(r,20));return await window.__m365crawlEngageCapture.stop();`, &got)
			if got.Fatal != tc.fatal {
				t.Fatalf("response fault fatal=%q, want %q", got.Fatal, tc.fatal)
			}
		})
	}
}

func TestCaptureSourceOrderAndTerminalCloneCancellation(t *testing.T) {
	var order struct{ IDs []string }
	runCapture(t, nativeFixtureJS+`
		await send(viewer);
		const delayed=JSON.stringify({data:{thread:{...thread,id:"first"}}});
		replies.push({fixtureResponse:new Response(new ReadableStream({start(c){setTimeout(()=>{c.enqueue(new TextEncoder().encode(delayed));c.close();},30);}}),{headers:{"content-type":"application/json"}})});
		const one=window.fetch("https://engage.cloud.microsoft/graphql");
		const second={...thread,id:"second"};
		replies.push({data:{thread:second}});
		const two=window.fetch(new URL("https://engage.cloud.microsoft/graphql"));
		await Promise.all([one.then(r=>r.text()),two.then(r=>r.text())]);
		await new Promise(r=>setTimeout(r,30));
		const result=await window.__m365crawlEngageCapture.stop();
		return {IDs:result.Threads.map(t=>t.ID)};
	`, &order)
	if len(order.IDs) != 2 || order.IDs[0] != "first" || order.IDs[1] != "second" {
		t.Fatalf("asynchronous source order: %+v", order)
	}
	var cancelled struct{ Fatal string }
	runCapture(t, nativeFixtureJS+`
		await send(viewer);
		const response=new Response(new ReadableStream({start(c){setTimeout(()=>{c.enqueue(new TextEncoder().encode(JSON.stringify({data:{thread}})));c.close();},30);}}),{headers:{"content-type":"application/json"}});
		replies.push({fixtureResponse:response});
		const appResponse=await window.fetch(new Request("https://engage.cloud.microsoft/graphql"));
		const appRead=appResponse.text();
		const result=await window.__m365crawlEngageCapture.stop();
		await appRead;
		return result;
	`, &cancelled)
	if cancelled.Fatal != "capture_incomplete" {
		t.Fatalf("partial clone published: %+v", cancelled)
	}
}
