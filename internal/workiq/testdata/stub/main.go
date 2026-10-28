// This synthetic executable speaks fetch-only MCP. It never uses the network.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

var kinds = []string{"inbox", "sentitems", "drafts", "deleteditems", "junkemail", "archive"}

func folder(kind string) map[string]any {
	total := 0
	if kind == "inbox" {
		total = 1
		if strings.HasPrefix(os.Getenv("WORKIQ_ACQUISITION_MODE"), "removed") {
			total = 0
		}
		if n, err := strconv.Atoi(os.Getenv("WORKIQ_ACQUISITION_MESSAGE_COUNT")); err == nil && n > 0 && n <= 10000 {
			total = n
		}
	}
	if kind == "sentitems" && (os.Getenv("WORKIQ_ACQUISITION_MODE") == "removed-move" || os.Getenv("WORKIQ_ACQUISITION_MODE") == "removed-new") {
		total = 1
	}
	children := 0
	if kind == "inbox" {
		children = 1
	}
	return map[string]any{"id": "folder-" + kind, "displayName": kind, "childFolderCount": children, "totalItemCount": total, "isHidden": kind == "archive"}
}

func fetch(path string) (any, int) {
	if path == "/me" {
		switch os.Getenv("WORKIQ_ACQUISITION_ME") {
		case "fail":
			return nil, 403
		case "missing-id":
			return map[string]any{"mail": "test.user@example.invalid"}, 200
		case "invalid-address":
			return map[string]any{"id": "synthetic-user", "mail": "not an address"}, 200
		case "pinned-mismatch":
			if len(os.Args) == 4 {
				return map[string]any{"id": "other-user", "mail": "test.user@example.invalid"}, 200
			}
		}
		return map[string]any{"id": "synthetic-user", "mail": "test.user@example.invalid", "userPrincipalName": "test.user@example.invalid"}, 200
	}
	if os.Getenv("WORKIQ_ACQUISITION_CHATS") != "" {
		mode := os.Getenv("WORKIQ_ACQUISITION_CHATS")
		if strings.HasPrefix(path, "/me/chats") && mode == "page-limit" {
			return map[string]any{"value": []any{}, "@odata.nextLink": "/me/chats?$skiptoken=loop"}, 200
		}
		if strings.HasPrefix(path, "/me/chats") && (mode == "count-limit" || mode == "byte-limit") {
			u, err := url.ParseRequestURI(path)
			if err != nil {
				os.Exit(89)
			}
			page, _ := strconv.Atoi(u.Query().Get("$skiptoken"))
			count, topic := 25001, ""
			if mode == "byte-limit" {
				count, topic = 128, strings.Repeat("x", 65536)
			}
			var values []any
			for i := 0; i < count; i++ {
				values = append(values, map[string]any{"id": fmt.Sprintf("bounded-%d-%d", page, i), "tenantId": "unarchived", "chatType": "group", "topic": topic})
			}
			return map[string]any{"value": values, "@odata.nextLink": fmt.Sprintf("/me/chats?$skiptoken=%d", page+1)}, 200
		}
		switch path {
		case "/me/chats":
			if mode == "discovery-fail" {
				return nil, 400
			}
			if os.Getenv("WORKIQ_ACQUISITION_CHATS") == "empty" {
				return map[string]any{"value": []any{}}, 200
			}
			if os.Getenv("WORKIQ_ACQUISITION_CHATS") == "malformed" {
				return map[string]any{"value": "not-a-collection"}, 200
			}
			return map[string]any{"value": []any{map[string]any{"id": "server-only", "tenantId": "synthetic-tenant", "chatType": "group"}},
				"@odata.nextLink": "/me/chats?$skiptoken=second"}, 200
		case "/me/chats?$skiptoken=second":
			id, tenant := "chat", "synthetic-tenant"
			switch os.Getenv("WORKIQ_ACQUISITION_CHATS") {
			case "duplicate":
				id = "server-only"
			case "unmatched":
				tenant = "unarchived-tenant"
			case "missing-tenant":
				tenant = ""
			case "bad-entity":
				id = ""
			}
			values := []any{map[string]any{"id": id, "tenantId": tenant, "chatType": "group"}}
			if mode == "tie" {
				values = append(values, map[string]any{"id": "another-empty", "tenantId": tenant, "chatType": "group"})
			}
			return map[string]any{"value": values}, 200
		case "/chats/chat/messages":
			return map[string]any{"value": []any{map[string]any{"id": "old-first", "createdDateTime": "2025-01-01T00:00:00Z"}},
				"@odata.nextLink": "/chats/chat/messages?$skiptoken=second"}, 200
		case "/chats/chat/messages?$skiptoken=second":
			if os.Getenv("WORKIQ_ACQUISITION_CHATS") == "fail-second" {
				return nil, 400
			}
			out := map[string]any{"value": []any{
				map[string]any{"id": "current-one", "createdDateTime": "2026-09-10T00:00:00Z"},
				map[string]any{"id": "current-two", "createdDateTime": "2026-09-11T00:00:00Z"},
			}}
			if mode == "delta" {
				out["@odata.deltaLink"] = "/chats/chat/messages?$deltatoken=end"
			}
			return out, 200
		case "/chats/server-only/messages":
			return map[string]any{"value": []any{}}, 200
		case "/chats/another-empty/messages", "/chats/chat/messages?$deltatoken=end":
			return map[string]any{"value": []any{}}, 200
		}
	}
	if path == "/me/mailFolders/folder-inbox/childFolders?includeHiddenFolders=true" {
		if os.Getenv("WORKIQ_ACQUISITION_MODE") == "child-fail" {
			return nil, 400
		}
		id := "a-child"
		if os.Getenv("WORKIQ_ACQUISITION_MODE") == "duplicate-child" {
			id = "folder-archive"
		}
		return map[string]any{"value": []any{map[string]any{"id": id, "parentFolderId": "folder-inbox", "childFolderCount": 0, "totalItemCount": 0}}}, 200
	}
	if path == "/me/mailFolders/a-child/messages/delta()" || path == "/me/mailFolders/a-child/messages/delta()?$deltatoken=end" {
		return map[string]any{"value": []any{}, "@odata.deltaLink": "/me/mailFolders/a-child/messages/delta()?$deltatoken=end"}, 200
	}
	root := "/me/mailFolders?includeHiddenFolders=true"
	if path == root || path == root+"&$skiptoken=second" {
		start, end := 0, 3
		if path != root {
			start, end = 3, 6
		}
		var values []any
		for _, kind := range kinds[start:end] {
			values = append(values, folder(kind))
		}
		out := map[string]any{"value": values}
		if start == 0 {
			out["@odata.nextLink"] = root + "&$skiptoken=second"
		}
		return out, 200
	}
	for _, kind := range kinds {
		if path == "/me/mailFolders/"+kind {
			return folder(kind), 200
		}
		base := "/me/mailFolders/folder-" + kind + "/messages/delta()"
		if path == base || path == base+"?$deltatoken=stable%2bstate" {
			if kind == "sentitems" && (os.Getenv("WORKIQ_ACQUISITION_MODE") == "removed-move" || os.Getenv("WORKIQ_ACQUISITION_MODE") == "removed-new") {
				id, header := "graph-moved", "<one@example.invalid>"
				if os.Getenv("WORKIQ_ACQUISITION_MODE") == "removed-new" {
					id, header = "graph-new-item", "<second@example.invalid>"
				}
				return map[string]any{"value": []any{map[string]any{
					"id": id, "parentFolderId": "folder-sentitems", "internetMessageId": header,
					"subject": "Synthetic server mail", "sentDateTime": "2026-09-10T07:59:00Z",
					"receivedDateTime": "2026-09-10T08:00:00Z", "lastModifiedDateTime": "2026-09-10T08:02:00Z",
					"from": map[string]any{"emailAddress": map[string]any{"name": "Synthetic sender", "address": "sender@example.invalid"}},
					"body": map[string]any{"contentType": "html", "content": "<p>retained synthetic server body</p>"},
				}}, "@odata.deltaLink": base + "?$deltatoken=stable%2bstate"}, 200
			}
			if kind == "inbox" && strings.HasPrefix(os.Getenv("WORKIQ_ACQUISITION_MODE"), "removed") {
				values := []any{}
				if strings.Contains(path, "$deltatoken=") {
					values = append(values, map[string]any{"id": "graph-one", "@removed": map[string]any{"reason": "deleted"}})
				}
				return map[string]any{"value": values, "@odata.deltaLink": base + "?$deltatoken=stable%2bstate"}, 200
			}
			if kind == "inbox" {
				if strings.Contains(path, "$deltatoken=") {
					if name := os.Getenv("WORKIQ_ACQUISITION_EXPIRE_ONCE"); name != "" {
						f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
						if err == nil {
							_ = f.Close()
							return nil, 410
						}
						if !os.IsExist(err) {
							os.Exit(87)
						}
					}
				}
				switch os.Getenv("WORKIQ_ACQUISITION_MODE") {
				case "permanent":
					return nil, 400
				case "throttled":
					return nil, 429
				case "transient-always":
					return nil, 504
				}
				if name := os.Getenv("WORKIQ_ACQUISITION_FAIL_ONCE"); name != "" {
					f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
					if err == nil {
						_ = f.Close()
						return nil, 504
					}
					if !os.IsExist(err) {
						os.Exit(85)
					}
				}
			}
			values := []any{}
			if kind == "inbox" && path == base {
				count := 1
				if n, err := strconv.Atoi(os.Getenv("WORKIQ_ACQUISITION_MESSAGE_COUNT")); err == nil && n > 0 && n <= 10000 {
					count = n
				}
				for i := 0; i < count; i++ {
					id, header := "graph-one", "<one@example.invalid>"
					if i > 0 {
						id, header = fmt.Sprintf("graph-%d", i), fmt.Sprintf("<item-%d@example.invalid>", i)
					}
					if os.Getenv("WORKIQ_ACQUISITION_MODE") == "no-message-id" {
						header = ""
					}
					values = append(values, map[string]any{
						"id": id, "parentFolderId": "folder-inbox", "internetMessageId": header,
						"subject": "Synthetic server mail", "sentDateTime": "2026-09-10T07:59:00Z",
						"receivedDateTime": "2026-09-10T08:00:00Z", "lastModifiedDateTime": "2026-09-10T08:01:00Z",
						"from":                         map[string]any{"emailAddress": map[string]any{"name": "Synthetic sender", "address": "sender@example.invalid"}},
						"body":                         map[string]any{"contentType": "html", "content": "<p>retained synthetic server body</p>"},
						"@microsoft.graph.downloadUrl": "https://example.invalid/private?sig=synthetic-fixture",
					})
				}
			}
			return map[string]any{"value": values, "@odata.deltaLink": base + "?$deltatoken=stable%2bstate"}, 200
		}
	}
	return nil, 400
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println("1.0.1")
		return
	}
	if strings.Join(os.Args[1:], " ") != "mcp" &&
		strings.Join(os.Args[1:], " ") != "mcp --account test.user@example.invalid" {
		os.Exit(80)
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Protocol string         `json:"protocolVersion"`
				Caps     map[string]any `json:"capabilities"`
				Name     string         `json:"name"`
				Args     struct {
					URLs []string `json:"entityUrls"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if decoder.Decode(&request) != nil {
			return
		}
		if path := os.Getenv("WORKIQ_ACQUISITION_LOCK_PATH"); path != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			release, err := store.AcquireWriter(ctx, path, store.WriterOrdinarySync)
			cancel()
			if err != nil {
				os.Exit(86)
			}
			release()
		}
		if request.Method == "notifications/initialized" && len(request.ID) == 0 {
			continue
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			if request.Params.Protocol != "2025-06-18" || len(request.Params.Caps) != 0 {
				os.Exit(81)
			}
			reply["result"] = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "synthetic-acquisition", "version": "1"}}
		case "tools/call":
			if request.Params.Name != "fetch" || len(request.Params.Args.URLs) != 1 {
				os.Exit(82)
			}
			path := request.Params.Args.URLs[0]
			if name := os.Getenv("WORKIQ_ACQUISITION_TRANSCRIPT"); name != "" {
				f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
				if err != nil {
					os.Exit(83)
				}
				_ = json.NewEncoder(f).Encode(map[string]any{"tool": "fetch", "path": path, "pinned": len(os.Args) == 4})
				_ = f.Close()
			}
			data, status := fetch(path)
			reply["result"] = map[string]any{"structuredContent": map[string]any{"results": []any{
				map[string]any{"data": data, "statusCode": status},
			}}}
		default:
			os.Exit(84)
		}
		if encoder.Encode(reply) != nil {
			return
		}
	}
}
