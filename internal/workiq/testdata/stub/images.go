package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"strings"
)

var imageObservations int

func imageFetch(path string) (any, int, bool) {
	mode := os.Getenv("WORKIQ_IMAGE_CASE")
	if mode == "" {
		return nil, 0, false
	}
	switch path {
	case "/me":
		user := "synthetic-user"
		address := "test.user@example.invalid"
		if mode == "wrong-user" {
			user = "another-user"
		}
		if mode == "invalid-address" {
			address = "not an address"
		}
		return map[string]any{"id": user, "mail": address}, 200, true
	case "/chats/chat":
		tenant, id, kind := "synthetic-tenant", "chat", "group"
		switch mode {
		case "wrong-tenant":
			tenant = "another-tenant"
		case "wrong-chat":
			id = "another-chat"
		case "channel":
			kind = "channel"
		case "unknown-kind":
			kind = ""
		case "malformed-chat":
			id = ""
		}
		return map[string]any{"id": id, "tenantId": tenant, "chatType": kind}, 200, true
	case "/chats/chat/messages/image-message":
		imageObservations++
		hosted := "selected-content"
		if mode == "changed-reference" && imageObservations == 2 {
			hosted = "different-content"
		}
		body := `<img itemtype="http://schema.skype.com/AMSImage" itemid="synthetic-image" src="/chats/chat/messages/image-message/hostedContents/` + hosted + `/$value">`
		if mode == "unsafe-reference" {
			body = strings.Replace(body, "/chats/chat/", "https://example.invalid/private?sig=private-ticket", 1)
		}
		message := map[string]any{"id": "image-message", "messageType": "message",
			"body": map[string]any{"contentType": "html", "content": body}}
		if mode != "clockless" && (mode != "lost-clock" || imageObservations == 1) {
			clock := "2026-11-01T10:00:00Z"
			if mode == "changed-clock" && imageObservations == 2 {
				clock = "2026-11-01T10:01:00Z"
			}
			message["lastModifiedDateTime"] = clock
		}
		if mode == "deleted" || mode == "deleted-after-blob" && imageObservations == 2 {
			message["deletedDateTime"] = "2026-11-01T10:02:00Z"
		}
		return message, 200, true
	}
	return nil, 404, true
}

func imageTool(name, path string) any {
	mode := os.Getenv("WORKIQ_IMAGE_CASE")
	if mode == "" || name != "fetch_blob" {
		return nil
	}
	if path != "/chats/chat/messages/image-message/hostedContents/selected-content/$value" {
		os.Exit(89)
	}
	recordDocumentCall(map[string]any{"tool": name, "path": path})
	if mode == "blob-failure" {
		return map[string]any{"isError": true, "content": []any{map[string]any{
			"type": "text", "text": "private native response https://example.invalid/?sig=private-ticket"}}}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		os.Exit(94)
	}
	contentType := "image/png"
	if mode == "wrong-format" {
		contentType = "image/jpeg"
	}
	if mode == "invalid-bytes" {
		data.Reset()
		data.WriteString("not a PNG; private native bytes")
	}
	return map[string]any{"structuredContent": map[string]any{
		"base64Content": base64.StdEncoding.EncodeToString(data.Bytes()), "blobName": "selected.png",
		"contentType": contentType, "sizeBytes": data.Len(), "statusCode": 200}}
}
