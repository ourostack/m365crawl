package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
)

// Synthetic document answers in WorkIQ 1.0.1's observed shapes (2026-10-30), selected by
// WORKIQ_DOCUMENT_CASE. Every call is appended to WORKIQ_DOCUMENT_TRANSCRIPT when set.
const (
	stubDrive  = "b!syntheticDrive0001"
	stubItem   = "SYNTHETICITEM0001"
	stubWebURL = "https://contoso-my.sharepoint.com/personal/test_user/Documents/tests/Synthetic plan.docx"
	stubLabel  = "9fbde396-1a24-4c79-8edf-9254a0f35055"
	stubText   = "Synthetic dummy text: purple walrus marmalade sentinel 7311."
)

func documentCase() string { return os.Getenv("WORKIQ_DOCUMENT_CASE") }

func recordDocumentCall(entry map[string]any) {
	name := os.Getenv("WORKIQ_DOCUMENT_TRANSCRIPT")
	if name == "" {
		return
	}
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(87)
	}
	_ = json.NewEncoder(f).Encode(entry)
	_ = f.Close()
}

// documentItem answers the share resolution fetch.
func documentItem(path string) (any, int, bool) {
	if !strings.HasPrefix(path, "/shares/u!") {
		return nil, 0, false
	}
	recordDocumentCall(map[string]any{"tool": "fetch", "path": path})
	if documentCase() == "missing" {
		return nil, 404, true
	}
	item := map[string]any{"id": stubItem, "name": "Synthetic plan.docx", "webUrl": stubWebURL, "size": 3681,
		"file":            map[string]any{"mimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		"parentReference": map[string]any{"driveId": stubDrive, "driveType": "business"}}
	switch documentCase() {
	case "folder":
		delete(item, "file")
		item["folder"] = map[string]any{"childCount": 1}
	case "large-unlabeled":
		item["size"] = 5 << 20
	}
	return item, 200, true
}

type capability = struct {
	Name  string `json:"name"`
	Items []struct {
		URL string `json:"url"`
	} `json:"items_by_url"`
}

func documentTool(name, query, path string, caps []capability) any {
	switch name {
	case "retrieve":
		if query == "" || len(caps) != 1 || caps[0].Name != "OneDriveAndSharePoint" || len(caps[0].Items) != 1 || caps[0].Items[0].URL != stubWebURL {
			os.Exit(88)
		}
		recordDocumentCall(map[string]any{"tool": "retrieve", "query": query, "url": caps[0].Items[0].URL})
		return retrieval()
	case "fetch_blob":
		if path != "/drives/"+stubDrive+"/items/"+stubItem+"/content" {
			os.Exit(89)
		}
		recordDocumentCall(map[string]any{"tool": "fetch_blob", "path": path})
		return blob()
	}
	return nil
}

func hit() map[string]any {
	return map[string]any{"contentProtection": "allowed", "driveId": stubDrive, "itemId": stubItem, "entityType": "Files", "id": "h1",
		"entityUrl": "/drives/" + stubDrive + "/items/" + stubItem,
		"markdown":  "- **Synthetic plan**\n  - **author:** Synthetic Author\n  - **fileType:** docx\n  - **snippet:** " + stubText,
		"sensitivityLabel": map[string]any{"color": "#FF8C00", "displayName": "Confidential\\Internal Only", "id": stubLabel,
			"isEncrypted": true, "priority": 6, "tooltip": "Data is classified and protected."},
		"webUrl": stubWebURL}
}

func retrieval() any {
	h := hit()
	results := []any{h}
	switch documentCase() {
	case "refused":
		h["contentProtection"] = "blocked"
	case "reference":
		results = []any{map[string]any{"driveId": stubDrive, "itemId": stubItem, "entityType": "Files", "id": "h1",
			"markdown": "- **Synthetic plan.docx**\n  - **typeDescription:** Word\n  - **referenceType:** 3", "webUrl": stubWebURL}}
	case "other-item":
		h["itemId"] = "SYNTHETICOTHER0002"
	case "no-hit":
		results = []any{}
	case "unlabeled-plain", "unlabeled-encrypted", "unlabeled-blob-missing", "unlabeled-blob-error", "large-unlabeled":
		delete(h, "sensitivityLabel")
	case "unencrypted-label":
		h["sensitivityLabel"] = map[string]any{"displayName": "General", "id": "f42aa342-8706-4288-bd11-ebb85995028c", "isEncrypted": false}
	case "truncated":
		h["markdown"] = "- **Synthetic plan**\n  - **truncationInfo:** Content has been truncated\n  - **snippet:** " + stubText
	case "tool-error":
		return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "Unhandled server failure"}}}
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": "synthetic"}},
		"structuredContent": map[string]any{"application/vnd.ms-workiq.retrieval": map[string]any{
			"resultCount": len(results), "results": results, "stoppedReason": "completed"}}}
}

func blob() any {
	data := []byte("PK\x03\x04synthetic-ooxml")
	switch documentCase() {
	case "unlabeled-encrypted":
		data = append([]byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, []byte("E\x00n\x00c\x00r\x00y\x00p\x00t\x00e\x00d\x00P\x00a\x00c\x00k\x00a\x00g\x00e\x00")...)
	case "unlabeled-blob-missing":
		return map[string]any{"structuredContent": map[string]any{"statusCode": 404}}
	case "unlabeled-blob-error":
		return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "Unhandled server failure"}}}
	}
	return map[string]any{"structuredContent": map[string]any{"base64Content": base64.StdEncoding.EncodeToString(data),
		"blobName": "Synthetic plan.docx", "contentType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"sizeBytes": len(data), "statusCode": 200}}
}
