package engagecontent

import "testing"

func TestCaptureSelectedStringAndTextBounds(t *testing.T) {
	for _, tc := range []struct{ name, setup, fatal string }{
		{"field-exact", `thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:[{text:"x".repeat(262144)}]});`, ""},
		{"field-plus-one", `thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:[{text:"x".repeat(262145)}]});`, "too_large"},
		{"utf8-field-exact", `thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:[{text:"\u00e9".repeat(131072)}]});`, ""},
		{"utf8-field-plus-one", `thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:[{text:"\u00e9".repeat(131072)+"x"}]});`, "too_large"},
		{"unpaired-surrogate", `thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:[{text:"\ud800"}]});`, "malformed"},
		{"nul-text", `thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:[{text:"x\u0000y"}]});`, "malformed"},
		{"text-exact", `thread.threadStarter.languageSpecificContent.title=null; thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:Array.from({length:4},()=>({text:"x".repeat(262144)}))});`, ""},
		{"text-plus-one", `thread.threadStarter.languageSpecificContent.title=null; thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:[...Array.from({length:4},()=>({text:"x".repeat(262144)})),{text:"y"}]});`, "too_large"},
		{"blocks-exact", `thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:Array.from({length:4096},()=>({text:""}))});`, ""},
		{"blocks-plus-one", `thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:Array.from({length:4097},()=>({text:""}))});`, "too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got struct{ Fatal string }
			runCapture(t, nativeFixtureJS+tc.setup+`await send(viewer); await send({thread}); return await window.__m365crawlEngageCapture.stop();`, &got)
			if got.Fatal != tc.fatal {
				t.Fatalf("selected boundary fatal=%q, want %q", got.Fatal, tc.fatal)
			}
		})
	}
}

func TestCaptureCandidateAndIgnoredNodeBounds(t *testing.T) {
	for _, tc := range []struct{ name, setup, fatal string }{
		{"threads-exact", `await send(viewer); await send({threads:Array.from({length:128},()=>thread)});`, ""},
		{"threads-plus-one", `await send(viewer); await send({threads:Array.from({length:129},()=>thread)});`, "too_large"},
		{"nodes-exact", `await send({...viewer,ignored:Array(65529).fill(null)});`, ""},
		{"nodes-plus-one", `await send({...viewer,ignored:Array(65530).fill(null)});`, "too_large"},
		{"depth-exact", `let nested=null; for(let i=0;i<127;i++)nested={next:nested}; await send({...viewer,ignored:nested});`, ""},
		{"depth-plus-one", `let nested=null; for(let i=0;i<128;i++)nested={next:nested}; await send({...viewer,ignored:nested});`, "too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got struct{ Fatal string }
			runCapture(t, nativeFixtureJS+tc.setup+`return await window.__m365crawlEngageCapture.stop();`, &got)
			if got.Fatal != tc.fatal {
				t.Fatalf("candidate boundary fatal=%q, want %q", got.Fatal, tc.fatal)
			}
		})
	}
}

func TestCaptureZeroTextAggregateAndIgnoredDraftDepth(t *testing.T) {
	for _, tc := range []struct{ name, setup, fatal string }{
		{"zero-blocks-consume-nodes", `thread.threadStarter.languageSpecificContent.title=null; thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:Array.from({length:4096},()=>({text:""}))}); await send(viewer); await send({threads:Array.from({length:16},()=>thread)});`, "too_large"},
		{"blocks-total-plus-one", `thread.threadStarter.languageSpecificContent.title=null; thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:Array.from({length:4096},()=>({text:""}))}); await send(viewer); await send({threads:Array.from({length:16},()=>thread)}); thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:[{text:""}]}); await send({thread});`, "too_large"},
		{"ignored-draft-depth", `let nested=null; for(let i=0;i<129;i++)nested={next:nested}; thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:[{text:""}],ignored:nested}); await send(viewer); await send({thread});`, "too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got struct{ Fatal string }
			runCapture(t, nativeFixtureJS+tc.setup+`return await window.__m365crawlEngageCapture.stop();`, &got)
			if got.Fatal != tc.fatal {
				t.Fatalf("zero/ignored bound=%q, want %q", got.Fatal, tc.fatal)
			}
		})
	}
}
