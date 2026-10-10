package sharepointcontent

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNativeIdentityCapture(t *testing.T) {
	ctx, page := scriptPage(t)
	input := admittedRequest{Kind: "page", Host: "fixture.sharepoint.com", Site: "/sites/sample", Path: "/sites/sample/SitePages/A B'.aspx"}
	args, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	expression := `(async args => {
		const location={origin:"https://fixture.sharepoint.com",hostname:"fixture.sharepoint.com"};
		const cases=["ok","wrong-path","wrong-file-id","wrong-account","missing-account","invalid-modified","file-denied"];
		const results=[];
		for(const kind of cases) {
			const calls=[];
			const payloads=[
				{UniqueId:"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",ServerRelativeUrl:args.Path,TimeLastModified:"2026-01-01T00:00:00Z",VroomDriveID:"drive-fixture",VroomItemID:"item-fixture",Length:"123",ListItemAllFields:{Id:7,Title:"Page"}},
				{Id:"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
				{Id:"cccccccc-cccc-cccc-cccc-cccccccccccc"},
				{Id:11,LoginName:"fixture-account"}
			];
			if(kind==="wrong-path") payloads[0].ServerRelativeUrl="/sites/other/Page.aspx";
			if(kind==="wrong-file-id") payloads[0].UniqueId="not-a-uuid";
			if(kind==="wrong-account") payloads[3].Id="11";
			if(kind==="missing-account") delete payloads[3].LoginName;
			if(kind==="invalid-modified") payloads[0].TimeLastModified="unparsed";
			const fetch=async (url,options) => {
				const ordinal=calls.length;
				calls.push({url:String(url),method:options.method,credentials:options.credentials});
				const response=new Response(JSON.stringify(payloads[ordinal]),{status:kind==="file-denied"&&ordinal===0?403:200,headers:{"content-type":"application/json"}});
				Object.defineProperty(response,"url",{value:String(url)});
				return response;
			};
			const factory=(` + networkJS + `);
			const result=await (` + captureJS + `)(args,factory);
			results.push({kind,result,calls});
		}
		return results;
	})(` + string(args) + `)`
	var got []struct {
		Kind   string
		Result scriptResult
		Calls  []struct{ URL, Method, Credentials string }
	}
	if err := page.Eval(ctx, expression, &got); err != nil {
		t.Fatal("synthetic native identity evaluation failed")
	}
	if len(got) != 7 {
		t.Fatalf("missing identity cases: %d", len(got))
	}
	if got[0].Result.State != "metadata_only" || got[0].Result.File.FileID != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" ||
		got[0].Result.Account.ID != 11 || got[0].Result.Account.LoginName != "fixture-account" ||
		len(got[0].Calls) != 4 {
		t.Fatalf("native identity not retained: %#v", got[0])
	}
	wantPath := "/sites/sample/_api/web/GetFileByServerRelativePath(decodedurl=%27%2Fsites%2Fsample%2FSitePages%2FA%20B%27%27.aspx%27)"
	if len(got[0].Calls[0].URL) < len("https://fixture.sharepoint.com"+wantPath) ||
		got[0].Calls[0].URL[:len("https://fixture.sharepoint.com"+wantPath)] != "https://fixture.sharepoint.com"+wantPath {
		t.Fatalf("native OData path changed: %s", got[0].Calls[0].URL)
	}
	for _, item := range got {
		for _, call := range item.Calls {
			if call.Method != "GET" || call.Credentials != "same-origin" {
				t.Fatalf("native identity request changed: %#v", call)
			}
		}
	}
	for i, code := range []string{"identity_mismatch", "malformed", "malformed", "malformed"} {
		if got[i+1].Result.State != code || got[i+1].Result.Account != (SourceAccount{}) || got[i+1].Result.File != (NativeFile{}) {
			t.Fatalf("invalid identity published native content: %#v", got[i+1])
		}
	}
	if got[5].Result.State != "metadata_only" || !reflect.DeepEqual(got[5].Result.Losses, []Loss{{Code: "modified_time_unmapped", Count: 1}}) {
		t.Fatalf("unknown modification time fabricated or lost: %#v", got[5])
	}
	if got[6].Result.State != "no_access" || len(got[6].Calls) != 1 || got[6].Result.File != (NativeFile{}) {
		t.Fatalf("file denial became metadata success: %#v", got[6])
	}
}
