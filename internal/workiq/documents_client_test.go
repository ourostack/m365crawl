package workiq

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// documentStubResult answers the in-package stub's document modes in WorkIQ 1.0.1's shapes.
func documentStubResult(mode string, raw json.RawMessage) any {
	var params struct {
		Name string `json:"name"`
		Args struct {
			URLs  []string `json:"entityUrls"`
			Path  string   `json:"path"`
			Query string   `json:"query"`
			Caps  []struct {
				Name  string              `json:"name"`
				Items []map[string]string `json:"items_by_sharepoint_ids"`
			} `json:"capabilities"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(raw, &params)
	if mode == "doc-error" {
		return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "Unhandled server failure"}}}
	}
	switch params.Name {
	case "fetch":
		if len(params.Args.URLs) != 1 || !strings.HasPrefix(params.Args.URLs[0], "/shares/u!") {
			return map[string]any{"isError": true}
		}
		return map[string]any{"structuredContent": map[string]any{"results": []any{map[string]any{"statusCode": 200, "data": map[string]any{
			"id": "SYNTHETICITEM0001", "name": "Plan.docx", "webUrl": "https://contoso.sharepoint.com/sites/s/Plan.docx", "size": 10,
			"file": map[string]any{}, "parentReference": map[string]any{"driveId": "b!syntheticDrive0001"},
			"sharepointIds": map[string]any{"siteId": stubGUID(1), "webId": stubGUID(2), "listId": stubGUID(3), "listItemUniqueId": stubGUID(4)}}}}}}
	case "retrieve":
		if params.Args.Query != "Plan" || len(params.Args.Caps) != 1 || params.Args.Caps[0].Name != "OneDriveAndSharePoint" ||
			len(params.Args.Caps[0].Items) != 1 || params.Args.Caps[0].Items[0]["unique_id"] != stubGUID(4) || params.Args.Caps[0].Items[0]["site_id"] != stubGUID(1) ||
			params.Args.Caps[0].Items[0]["web_id"] != stubGUID(2) || params.Args.Caps[0].Items[0]["list_id"] != stubGUID(3) {
			return map[string]any{"isError": true}
		}
		return json.RawMessage(liveRetrieval)
	case "fetch_blob":
		if params.Args.Path != "/drives/b!syntheticDrive0001/items/SYNTHETICITEM0001/content" {
			return map[string]any{"isError": true}
		}
		if mode == "doc-404" {
			return map[string]any{"structuredContent": map[string]any{"statusCode": 404}}
		}
		return map[string]any{"structuredContent": map[string]any{"base64Content": base64.StdEncoding.EncodeToString([]byte("PK\x03\x04")), "sizeBytes": 4, "statusCode": 200}}
	}
	return map[string]any{"isError": true}
}

func TestDocumentCallsOverMCP(t *testing.T) {
	ctx := context.Background()
	useStub(t, "doc")
	client, err := Open(ctx, Invocation{ExplicitTeamsRoot: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := client.ResolveDocument(ctx, "https://contoso.sharepoint.com/:w:/s/s/Eabc")
	if err != nil || item.Ref.ItemID != "SYNTHETICITEM0001" || !item.File {
		t.Fatalf("resolve %+v %v", item, err)
	}
	hits, err := client.RetrieveDocument(ctx, item, "Plan")
	if err != nil || len(hits) != 1 || *hits[0].ContentProtection != "allowed" {
		t.Fatalf("retrieve %+v %v", hits, err)
	}
	data, status, err := client.FetchDocumentBlob(ctx, item.Ref)
	if err != nil || status != 200 || InspectOffice(data) != BytesNotEncrypted {
		t.Fatalf("blob %q %d %v", data, status, err)
	}
	if _, err := client.RetrieveDocument(ctx, item, " "); !hasCode(err, "workiq_protocol") {
		t.Fatalf("blank query: %v", err)
	}
	if _, _, err := client.FetchDocumentBlob(ctx, DocumentRef{DriveID: "x", ItemID: "y"}); !hasCode(err, "workiq_protocol") {
		t.Fatalf("bad ref: %v", err)
	}
	if _, _, err := client.ResolveDocument(ctx, "https://evil.example/x"); !hasCode(err, "document_link_unsupported") {
		t.Fatalf("bad link: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	useStub(t, "doc-404")
	client, err = Open(ctx, Invocation{ExplicitTeamsRoot: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, status, err := client.FetchDocumentBlob(ctx, item.Ref); status != 404 || !hasCode(err, "workiq_failed") {
		t.Fatalf("404: %d %v", status, err)
	}
	if _, err := client.RetrieveDocument(ctx, item, "Plan"); err != nil {
		t.Fatalf("a refused download closed the session: %v", err)
	}
	_ = client.Close()

	for _, call := range []func(*Client) error{
		func(c *Client) error {
			_, _, err := c.ResolveDocument(ctx, "https://contoso.sharepoint.com/:w:/s/s/Eabc")
			return err
		},
		func(c *Client) error { _, err := c.RetrieveDocument(ctx, item, "Plan"); return err },
		func(c *Client) error { _, _, err := c.FetchDocumentBlob(ctx, item.Ref); return err },
	} {
		useStub(t, "doc-error")
		client, err = Open(ctx, Invocation{ExplicitTeamsRoot: true}, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := call(client); !hasCode(err, "workiq_failed") {
			t.Fatalf("tool error: %v", err)
		}
		_ = client.Close()
	}
	for _, call := range []func(*Client) error{
		func(c *Client) error { _, err := c.RetrieveDocument(ctx, item, "Plan"); return err },
		func(c *Client) error { _, _, err := c.FetchDocumentBlob(ctx, item.Ref); return err },
	} {
		useStub(t, "doc")
		client, err = Open(ctx, Invocation{ExplicitTeamsRoot: true}, "")
		if err != nil {
			t.Fatal(err)
		}
		_ = client.Close()
		if err := call(client); err == nil {
			t.Fatal("a closed session answered")
		}
	}
}

func TestDocumentDecoderEdges(t *testing.T) {
	ctx := context.Background()
	huge := make([]byte, maxFrame+1)
	if _, err := decodeRetrieval(ctx, huge); !hasCode(err, "workiq_page_too_large") {
		t.Fatal(err)
	}
	if _, _, err := decodeBlob(ctx, huge); !hasCode(err, "workiq_page_too_large") {
		t.Fatal(err)
	}
	for _, body := range []string{`[]`, `{`} {
		if _, err := decodeRetrieval(ctx, []byte(body)); !hasCode(err, "workiq_protocol") {
			t.Fatal(body, err)
		}
		if _, _, err := decodeBlob(ctx, []byte(body)); !hasCode(err, "workiq_protocol") {
			t.Fatal(body, err)
		}
	}
	if _, _, err := decodeBlob(ctx, []byte(`{"isError":true,"content":[{"text":"x"}]}`)); !hasCode(err, "workiq_failed") {
		t.Fatal(err)
	}
	if _, err := decodeDocumentItem(ctx, json.RawMessage(`{`)); !hasCode(err, "workiq_protocol") {
		t.Fatal(err)
	}
	long := strings.Repeat("é", 300)
	body := `{"structuredContent":{"` + retrievalKey + `":{"results":[{"driveId":"b!abcdefgh12","itemId":"ABCDEFGH12","sensitivityLabel":{"id":"9FBDE396-1A24-4C79-8EDF-9254A0F35055","displayName":"` + long + `"}}]}}}`
	hits, err := decodeRetrieval(ctx, []byte(body))
	if err != nil || len([]rune(hits[0].Label.Name)) != 256 || hits[0].Label.ID != "9fbde396-1a24-4c79-8edf-9254a0f35055" || hits[0].Label.Encrypted != nil {
		t.Fatalf("%+v %v", hits, err)
	}
}

func stubGUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
