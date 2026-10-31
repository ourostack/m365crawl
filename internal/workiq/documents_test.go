package workiq

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

// liveRetrieval is WorkIQ 1.0.1's retrieve answer for an encrypted test document the user owns,
// observed 2026-10-30 (dummy text; identifiers are the test file's).
const liveRetrieval = `{"content":[{"type":"text","text":"..."}],"structuredContent":{"application/vnd.ms-workiq.retrieval":{"resultCount":1,"results":[{"contentProtection":"allowed","driveId":"b!2kdIZxq3p0SDWyXTw5A9FDg_I5t7LwBJoRaeA1JcnmPgwFpevPbKT6vwXCq0wbZt","entityType":"Files","entityUrl":"/drives/b!2kdIZxq3p0SDWyXTw5A9FDg_I5t7LwBJoRaeA1JcnmPgwFpevPbKT6vwXCq0wbZt/items/01LC55GA2YKE3IMU2MGNBZ2M2W2EJCTRJ5","id":"h1","itemId":"01LC55GA2YKE3IMU2MGNBZ2M2W2EJCTRJ5","markdown":"- **m365crawl-243-extract-test**\n  - **author:** Test Author\n  - **fileType:** docx\n  - **fileName:** m365crawl-243-extract-test.docx\n  - **snippet:** m365crawl issue 243 dummy test document A. Purple walrus marmalade sentinel 7311. This text is synthetic and contains nothing confidential.","resourceId":"AAAA","resourceMetadata":{"document":{"title":"m365crawl-243-extract-test"}},"sensitivityLabel":{"color":"#FF8C00","displayName":"Confidential\\Internal Only","id":"9fbde396-1a24-4c79-8edf-9254a0f35055","isEncrypted":true,"priority":6,"tooltip":"Data is classified and protected."},"webUrl":"https://contoso-my.sharepoint-df.com/personal/test/_layouts/15/Doc.aspx?sourcedoc=%7B86365158-4C53-4333-9D33-56D11229C53D%7D"}],"searchMetadata":{"searchTerms":["m365crawl"],"types":["File"]},"stoppedReason":"completed"}}}`

// liveReferenceOnly is the reference-only hit WorkIQ returned for an unprocessed encrypted file:
// no contentProtection, no label and no snippet.
const liveReferenceOnly = `{"structuredContent":{"application/vnd.ms-workiq.retrieval":{"resultCount":1,"results":[{"driveId":"b!2kdIZxq3p0SDWyXTw5A9FDg_I5t7LwBJoRaeA1JcnmPgwFpevPbKT6vwXCq0wbZt","entityType":"Files","id":"h1","itemId":"01LC55GA5OR5PRJ5I3IVHJJFXJENPANCHB","markdown":"- **m365crawl-243-viewonly-test.docx**\n  - **typeDescription:** Word\n  - **referenceType:** 3\n  - **fileType:** docx","resourceMetadata":{"document":{"title":"m365crawl-243-viewonly-test.docx"}},"webUrl":"https://contoso-my.sharepoint-df.com/personal/test/Documents/x/m365crawl-243-viewonly-test.docx"}],"searchMetadata":{"types":["File"]},"stoppedReason":"completed"}}}`

func TestDecodeRetrievalLiveShapes(t *testing.T) {
	hits, err := decodeRetrieval(context.Background(), []byte(liveRetrieval))
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
	h := hits[0]
	if h.Ref.ItemID != "01LC55GA2YKE3IMU2MGNBZ2M2W2EJCTRJ5" || h.ContentProtection == nil || *h.ContentProtection != "allowed" {
		t.Fatalf("hit=%+v", h)
	}
	if h.Label == nil || h.Label.ID != "9fbde396-1a24-4c79-8edf-9254a0f35055" || h.Label.Name != `Confidential\Internal Only` ||
		h.Label.Encrypted == nil || !*h.Label.Encrypted {
		t.Fatalf("label=%+v", h.Label)
	}
	if !strings.HasPrefix(h.Snippet, "m365crawl issue 243 dummy test document A.") || h.Truncated {
		t.Fatalf("snippet=%q truncated=%v", h.Snippet, h.Truncated)
	}

	hits, err = decodeRetrieval(context.Background(), []byte(liveReferenceOnly))
	if err != nil || len(hits) != 1 || hits[0].ContentProtection != nil || hits[0].Label != nil || hits[0].Snippet != "" {
		t.Fatalf("reference-only hits=%+v err=%v", hits, err)
	}
}

func TestDecodeRetrievalProtectionValues(t *testing.T) {
	for raw, want := range map[string]string{`"allowed"`: "allowed", `"blocked"`: "blocked", `"not allowed!"`: "other", `7`: "other"} {
		body := `{"structuredContent":{"` + retrievalKey + `":{"results":[{"driveId":"b!abcdefgh12","itemId":"ABCDEFGH12","contentProtection":` + raw + `}]}}}`
		hits, err := decodeRetrieval(context.Background(), []byte(body))
		if err != nil || len(hits) != 1 || hits[0].ContentProtection == nil || *hits[0].ContentProtection != want {
			t.Fatalf("%s: hits=%+v err=%v", raw, hits, err)
		}
	}
}

func TestDecodeRetrievalRefusesUnknownShapes(t *testing.T) {
	for name, body := range map[string]string{
		"missing key":   `{"structuredContent":{"results":[]}}`,
		"not json":      `{`,
		"bad label id":  `{"structuredContent":{"` + retrievalKey + `":{"results":[{"sensitivityLabel":{"id":"x"}}]}}}`,
		"results types": `{"structuredContent":{"` + retrievalKey + `":{"results":{}}}}`,
	} {
		if _, err := decodeRetrieval(context.Background(), []byte(body)); !hasCode(err, "workiq_protocol") {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if _, err := decodeRetrieval(context.Background(), []byte(`{"isError":true,"content":[{"type":"text","text":"Run workiq auth login"}]}`)); !hasCode(err, "workiq_signin_required") {
		t.Fatalf("tool error: %v", err)
	}
}

func TestRetrievalSnippet(t *testing.T) {
	text, cut := retrievalSnippet("- **T**\n  - **truncationInfo:** Content has been truncated\n  - **snippet:** one two\n  - **later:** x")
	if text != "one two" || !cut {
		t.Fatalf("%q %v", text, cut)
	}
	if text, cut := retrievalSnippet("- **T**\n  - **fileType:** docx"); text != "" || cut {
		t.Fatalf("%q %v", text, cut)
	}
}

func TestDocumentLink(t *testing.T) {
	path, err := DocumentLink("https://contoso.sharepoint.com/:w:/t/team/EabcDEF?e=1")
	if err != nil || !strings.HasPrefix(path, "/shares/u!") || !strings.HasSuffix(path, "/driveItem?$select=id,name,webUrl,size,file,parentReference,sharepointIds") {
		t.Fatalf("path=%q err=%v", path, err)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/shares/u!"), "/driveItem?$select=id,name,webUrl,size,file,parentReference,sharepointIds")
	if strings.ContainsAny(id, "=+/") {
		t.Fatalf("share id not unpadded base64url: %q", id)
	}
	if decoded, _ := base64.RawURLEncoding.DecodeString(id); string(decoded) != "https://contoso.sharepoint.com/:w:/t/team/EabcDEF?e=1" {
		t.Fatalf("decoded=%q", decoded)
	}
	for _, bad := range []string{"", "http://contoso.sharepoint.com/x", "https://evil.example/x", "https://contoso.sharepoint.com/",
		"https://user@contoso.sharepoint.com/x", "https://contoso.sharepoint.com:8443/x", "https://contoso.sharepoint.com/x#frag",
		"https://contoso.sharepoint.com/a b", "https://contoso.sharepoint.com.evil.example/x", strings.Repeat("a", 9000)} {
		if _, err := DocumentLink(bad); !hasCode(err, "document_link_unsupported") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := DocumentLink("https://contoso-my.sharepoint-df.com/personal/u/Documents/x.docx"); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeDocumentItem(t *testing.T) {
	good := `{"id":"01LC55GA2YKE3IMU2MGNBZ2M2W2EJCTRJ5","name":"a.docx","webUrl":"https://contoso-my.sharepoint.com/personal/u/Documents/a.docx","size":3681,"file":{"mimeType":"x"},"parentReference":{"driveId":"b!2kdIZxq3p0SDWyXTw5A9FDg"},"sharepointIds":{"listId":"5E5AC0E0-F6BC-4FCA-ABF0-5C2AB4C1B66D","listItemUniqueId":"86365158-4c53-4333-9d33-56d11229c53d","siteId":"674847da-b71a-44a7-835b-25d3c3903d14","webId":"9b233f38-2f7b-4900-a116-9e03525c9e63","siteUrl":"https://contoso-my.sharepoint.com/personal/u"}}`
	item, err := decodeDocumentItem(context.Background(), json.RawMessage(good))
	if err != nil || !item.File || item.Size != 3681 || item.Ref.DriveID != "b!2kdIZxq3p0SDWyXTw5A9FDg" ||
		item.SharePoint != (SharePointIDs{SiteID: "674847da-b71a-44a7-835b-25d3c3903d14", WebID: "9b233f38-2f7b-4900-a116-9e03525c9e63",
			ListID: "5e5ac0e0-f6bc-4fca-abf0-5c2ab4c1b66d", UniqueID: "86365158-4c53-4333-9d33-56d11229c53d"}) {
		t.Fatalf("item=%+v err=%v", item, err)
	}
	folder := strings.Replace(good, `"file":{"mimeType":"x"}`, `"folder":{}`, 1)
	if item, err := decodeDocumentItem(context.Background(), json.RawMessage(folder)); err != nil || item.File {
		t.Fatalf("folder item=%+v err=%v", item, err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"b!2kdIZxq3p0SDWyXTw5A9FDg"`, `"../x"`, 1),
		strings.Replace(good, `https://contoso-my.sharepoint.com`, `https://evil.example`, 1),
		strings.Replace(good, `"name":"a.docx"`, `"name":""`, 1),
		strings.Replace(good, `"size":3681`, `"size":-1`, 1),
		strings.Replace(good, `"listItemUniqueId":"86365158-4c53-4333-9d33-56d11229c53d",`, ``, 1),
	} {
		if _, err := decodeDocumentItem(context.Background(), json.RawMessage(bad)); !hasCode(err, "workiq_protocol") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestDecodeBlob(t *testing.T) {
	payload := []byte("PK\x03\x04rest")
	ok := `{"structuredContent":{"base64Content":"` + base64.StdEncoding.EncodeToString(payload) + `","blobName":"a.docx","contentType":"application/x","sizeBytes":8,"statusCode":200}}`
	data, status, err := decodeBlob(context.Background(), []byte(ok))
	if err != nil || status != 200 || string(data) != string(payload) {
		t.Fatalf("data=%q status=%d err=%v", data, status, err)
	}
	if _, status, err := decodeBlob(context.Background(), []byte(`{"structuredContent":{"statusCode":404}}`)); status != 404 || err == nil {
		t.Fatalf("404: status=%d err=%v", status, err)
	}
	for _, bad := range []string{
		strings.Replace(ok, `"sizeBytes":8`, `"sizeBytes":9`, 1),
		strings.Replace(ok, `"statusCode":200`, `"statusCode":206`, 1),
		`{"structuredContent":{"statusCode":200}}`,
	} {
		if _, _, err := decodeBlob(context.Background(), []byte(bad)); !hasCode(err, "workiq_protocol") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestInspectOffice(t *testing.T) {
	cfb := append(append([]byte{}, cfbSignature...), make([]byte, 64)...)
	if got := InspectOffice([]byte("PK\x03\x04...")); got != BytesNotEncrypted {
		t.Fatal(got)
	}
	if got := InspectOffice(append(append([]byte{}, cfb...), utf16le("EncryptedPackage")...)); got != BytesEncrypted {
		t.Fatal(got)
	}
	if got := InspectOffice(cfb); got != BytesUnknown {
		t.Fatal(got)
	}
	if got := InspectOffice([]byte("%PDF-1.7")); got != BytesUnknown {
		t.Fatal(got)
	}
}

func hasCode(err error, code string) bool {
	var coded *errs.Coded
	return errors.As(err, &coded) && coded.Code == code
}
