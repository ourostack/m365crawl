package engagecontent_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/ourostack/m365crawl/internal/engagecontent"
)

func TestSelectedNativeScriptAndDecode(t *testing.T) {
	id := base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Thread","id":"42"}`))
	target, err := engagecontent.ParseThreadURL("https://engage.cloud.microsoft/main/threads/" + id)
	if err != nil {
		t.Fatal(err)
	}
	previous := engagecontent.InitScriptForTest
	engagecontent.InitScriptForTest = engagecontent.SelectedScriptForTest(target)
	defer func() { engagecontent.InitScriptForTest = previous }()
	var raw engagecontent.ScriptResultForTest
	runCapture(t, nativeFixtureJS+fmt.Sprintf(`
	delete thread.__typename;
	thread.id=%q;
	thread.threadStarter.thread={id:thread.id};
	thread.threadStarter.sender={__typename:"User",id:"author",displayName:"Synthetic Author",network:{id:"network"}};
	thread.threadStarter.attachments={edges:[{node:{id:"attachment",title:"Synthetic title",__typename:"SyntheticNativeType",url:"https://example.test/attachment"}}],totalCount:1};
	thread.threadStarter.languageSpecificContent.body.serializedContentState=JSON.stringify({
		blocks:[{text:"starter https://example.test/a?sig=private",inlineStyleRanges:[{offset:0,length:7,style:"BOLD"}]}],entityMap:{0:{type:"LINK",data:{url:"https://example.test/safe"}}}});
	const reply=JSON.parse(JSON.stringify(thread.threadStarter));
	reply.id="reply";reply.parentMessage={id:"starter"};reply.attachments={edges:[],totalCount:0};
	reply.languageSpecificContent.body.serializedContentState=JSON.stringify({blocks:[{text:"reply search needle"}],entityMap:{}});
	thread.topLevelReplies={edges:[{node:reply}],totalCount:1,pageInfo:{hasNextPage:false,hasPreviousPage:false}};
	await send(viewer); await send({thread});
	return await window.__m365crawlEngageCapture.stop();`, id), &raw)
	got, err := engagecontent.DecodeSelectedForTest(context.Background(), target, raw)
	if err != nil {
		t.Fatal(err)
	}
	m := got.Threads[0].Selected.Messages
	if len(m) != 2 || len(m[0].Attachments) != 1 || len(m[0].Links) != 1 || m[1].Blocks[0] != "reply search needle" || m[0].Blocks[0] == "starter https://example.test/a?sig=private" {
		t.Fatal("selected native content projection missing")
	}
	found := false
	for _, loss := range got.Losses {
		if loss.Code == "formatting_unmapped" {
			found = true
		}
	}
	if !found {
		t.Fatal("omitted native inline formatting was not reported")
	}
}
