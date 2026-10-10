package sharepointcontent

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNativeStreamCollectionAndEntries(t *testing.T) {
	ctx, page := scriptPage(t)
	args, _ := json.Marshal(admittedRequest{Kind: "stream", Host: "fixture.sharepoint.com", Site: "/sites/sample", Path: "/sites/sample/Videos/Video.mp4"})
	expression := `(async args=>{
		const location={origin:"https://fixture.sharepoint.com",hostname:"fixture.sharepoint.com"};
		const cases=["ok","empty","multiple","selected","duplicate","next-link","foreign-download","denied-download"];
		const results=[];
		for(const kind of cases){
			const request={...args,TranscriptID:kind==="selected"?"second":""};
			const payloads=[
				{UniqueId:"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",ServerRelativeUrl:args.Path,VroomDriveID:"drive",VroomItemID:"item"},
				{Id:"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
				{Id:"cccccccc-cccc-cccc-cccc-cccccccccccc"},
				{Id:11,LoginName:"fixture-account"},
				{value:[{id:"first",temporaryDownloadUrl:"https://fixture.sharepoint.com/download?signed=private-fixture"}]},
				{entries:[{id:"entry-1",text:"First",startOffset:"00:00:01.125",endOffset:"00:00:02",speakerId:"ignored",speaker:"ignored"},
					{id:"entry-2",text:"Second",speakerDisplayName:"Native speaker",startOffset:"invalid",endOffset:null}]}];
			if(kind==="empty")payloads[4].value=[];
			if(kind==="multiple"||kind==="selected")payloads[4].value.push({id:"second",temporaryDownloadUrl:"https://fixture.sharepoint.com/second"});
			if(kind==="duplicate")payloads[4].value.push({...payloads[4].value[0]});
			if(kind==="next-link")payloads[4]["@odata.nextLink"]="https://fixture.sharepoint.com/more";
			if(kind==="foreign-download")payloads[4].value[0].temporaryDownloadUrl="https://other.sharepoint.com/download";
			const urls=[];
			const fetch=async url=>{
				const ordinal=urls.length;urls.push(String(url));
				const response=new Response(JSON.stringify(payloads[ordinal]),{status:kind==="denied-download"&&ordinal===5?403:200,headers:{"content-type":"application/json"}});
				Object.defineProperty(response,"url",{value:String(url)});return response;
			};
			const result=await (` + captureJS + `)(request,(` + networkJS + `));
			results.push({kind,result,urls});
		}
		return results;
	})(` + string(args) + `)`
	var got []struct {
		Kind   string
		Result scriptResult
		URLs   []string
	}
	if err := page.Eval(ctx, expression, &got); err != nil {
		t.Fatal("synthetic Stream script failed")
	}
	wantStates := []string{"transcript_observations", "no_transcript", "transcript_selection_required", "transcript_observations", "malformed", "collection_incomplete", "unsupported_download_host", "no_access"}
	states := make([]string, len(got))
	for i, item := range got {
		states[i] = item.Result.State
	}
	if !reflect.DeepEqual(states, wantStates) {
		t.Fatalf("Stream selection/admission changed: %#v", got)
	}
	if got[0].Result.Transcript == nil || got[0].Result.Transcript.ID != "first" || len(got[0].Result.Transcript.Entries) != 2 ||
		got[0].Result.Transcript.Entries[0].SpeakerDisplayName != nil || got[0].Result.Transcript.Entries[1].SpeakerDisplayName == nil {
		t.Fatalf("native entry/speaker fields changed: %#v", got[0])
	}
	if len(got[0].URLs) != 6 || got[0].URLs[4] != "https://fixture.sharepoint.com/sites/sample/_api/v2.0/drives/drive/items/item/media/transcripts" ||
		got[3].URLs[5] != "https://fixture.sharepoint.com/second?format=json" || len(got[6].URLs) != 5 {
		t.Fatalf("wrong collection/download route: %#v", got)
	}
}
