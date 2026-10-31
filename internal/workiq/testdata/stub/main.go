// This synthetic executable speaks WorkIQ's MCP: fetch, the document tools and the directed-write
// create_entity. It never uses the network.
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
	out := map[string]any{"id": "folder-" + kind, "displayName": kind, "childFolderCount": children, "totalItemCount": total, "isHidden": kind == "archive"}
	if os.Getenv("WORKIQ_PROBE_CASE") == "root-parent" {
		out["parentFolderId"] = "graph-root"
	}
	if os.Getenv("WORKIQ_PROBE_CASE") == "missing-total" {
		delete(out, "totalItemCount")
	}
	return out
}

// failFrom answers with a server failure: an MCP tool error without results, or a Graph 404 in
// WorkIQ's nested error shape with a private message. It fails every fetch whose path contains
// WORKIQ_ACQUISITION_FAIL_PATH, and, counting fetches across every child process in a shared file,
// every fetch from the Nth on.
func failFrom(path string) map[string]any {
	failure := map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "Unhandled server failure for subject Quarterly plan"}}}
	switch os.Getenv("WORKIQ_ACQUISITION_FAIL_SHAPE") {
	case "tool-error-request-id": // live: the same failure names a new request each time
		failure = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text",
			"text": fmt.Sprintf("Unhandled server failure request-id %08x-1111-2222-3333-444455556666", time.Now().UnixNano()&0xffffffff)}}}
	case "graph-404":
		failure = map[string]any{"isError": true, "structuredContent": map[string]any{"results": []any{map[string]any{"data": nil, "statusCode": 404,
			"error": map[string]any{"error": map[string]any{"code": "itemNotFound", "message": "The chat Quarterly plan was not found"}}}}}}
	}
	if pinned := os.Getenv("WORKIQ_ACQUISITION_FAIL_PATH"); pinned != "" && strings.Contains(path, pinned) {
		return failure
	}
	from, err := strconv.Atoi(os.Getenv("WORKIQ_ACQUISITION_FAIL_FROM"))
	if err != nil || from < 1 {
		return nil
	}
	counter := os.Getenv("WORKIQ_ACQUISITION_FAIL_COUNTER")
	f, err := os.OpenFile(counter, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: test-chosen counter path.
	if err != nil {
		os.Exit(90)
	}
	_, _ = f.Write([]byte{'.'})
	_ = f.Close()
	info, err := os.Stat(counter)
	if err != nil {
		os.Exit(91)
	}
	if info.Size() < int64(from) {
		return nil
	}
	return failure
}

// createOnce creates name and says whether this call created it.
func createOnce(name string) bool {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: test-chosen marker path.
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func fetch(path string) (any, int) {
	if os.Getenv("WORKIQ_PROBE_CASE") == "folder-limit" && strings.HasPrefix(path, "/me/mailFolders?") {
		u, _ := url.ParseRequestURI(path)
		page, _ := strconv.Atoi(u.Query().Get("$skiptoken"))
		values := []any{}
		for i := 0; i < 100; i++ {
			values = append(values, map[string]any{"id": fmt.Sprintf("%d-%d-", page, i) + strings.Repeat("x", 65500),
				"childFolderCount": 0, "totalItemCount": 0})
		}
		return map[string]any{"value": values, "@odata.nextLink": fmt.Sprintf("/me/mailFolders?includeHiddenFolders=true&$skiptoken=%d", page+1)}, 200
	}
	if os.Getenv("WORKIQ_PROBE_CASE") == "message-limit" && strings.HasPrefix(path, "/chats/chat/messages") {
		u, _ := url.ParseRequestURI(path)
		page, _ := strconv.Atoi(u.Query().Get("$skiptoken"))
		values := []any{}
		for i := 0; i < 100; i++ {
			values = append(values, map[string]any{"id": fmt.Sprintf("%d-%d-", page, i) + strings.Repeat("x", 65500),
				"createdDateTime": "2026-09-10T00:00:00Z"})
		}
		return map[string]any{"value": values, "@odata.nextLink": fmt.Sprintf("/chats/chat/messages?$skiptoken=%d", page+1)}, 200
	}
	if path == "/me" && len(os.Args) == 4 { // live: WorkIQ lost during the pinned identity check
		// After a child died mid-traversal (WORKIQ_ACQUISITION_DIE_MARK exists), every reopen dies too.
		_, marked := os.Stat(os.Getenv("WORKIQ_ACQUISITION_DIE_MARK"))
		if die := os.Getenv("WORKIQ_ACQUISITION_ME_DIE"); die != "" && (die == "always" || createOnce(die)) || os.Getenv("WORKIQ_ACQUISITION_DIE_MARK") != "" && marked == nil {
			_, _ = fmt.Fprintln(os.Stderr, "fatal: connection lost for user@example.invalid")
			os.Exit(3)
		}
	}
	if path == "/me" {
		switch os.Getenv("WORKIQ_ACQUISITION_ME") {
		case "unbound-other":
			if len(os.Args) != 4 || os.Args[3] == "other@example.invalid" {
				return map[string]any{"id": "other-user", "mail": "other@example.invalid"}, 200
			}
		case "pinned-other-address":
			if len(os.Args) == 4 {
				return map[string]any{"id": "other-user", "mail": "other@example.invalid"}, 200
			}
		case "fail":
			return nil, 403
		case "unavailable":
			return nil, 503
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
			switch os.Getenv("WORKIQ_PROBE_CASE") {
			case "status-bad-chat":
				return map[string]any{"value": []any{map[string]any{"id": ""}}}, 200
			case "status-mixed-chats":
				return map[string]any{"value": []any{
					map[string]any{"id": "one", "tenantId": "synthetic-tenant"},
					map[string]any{"id": "two", "tenantId": "synthetic-tenant"},
					map[string]any{"id": "other", "tenantId": "unarchived"},
				}}, 200
			}
			if mode == "discovery-fail" {
				return nil, 400
			}
			if os.Getenv("WORKIQ_ACQUISITION_CHATS") == "empty" {
				return map[string]any{"value": []any{}}, 200
			}
			if os.Getenv("WORKIQ_ACQUISITION_CHATS") == "malformed" {
				return map[string]any{"value": "not-a-collection"}, 200
			}
			first := map[string]any{"id": "server-only", "tenantId": "synthetic-tenant", "chatType": "group"}
			switch mode { // live: Graph repeats chats on later pages, unchanged or moved by new activity
			case "repeat-newer":
				first["lastUpdatedDateTime"] = "2026-09-10T00:00:00Z"
			case "repeat-older":
				first["lastUpdatedDateTime"] = "2026-09-11T00:00:00Z"
			}
			return map[string]any{"value": []any{first}, "@odata.nextLink": "/me/chats?$skiptoken=second"}, 200
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
			switch mode {
			case "repeat-newer":
				values = append([]any{map[string]any{"id": "server-only", "tenantId": "synthetic-tenant", "chatType": "group", "lastUpdatedDateTime": "2026-09-11T00:00:00Z"}}, values...)
			case "repeat-older":
				values = append([]any{map[string]any{"id": "server-only", "tenantId": "synthetic-tenant", "chatType": "group", "lastUpdatedDateTime": "2026-09-10T00:00:00Z"}}, values...)
			case "repeat-conflict":
				values = append([]any{map[string]any{"id": "server-only", "tenantId": "other-tenant", "chatType": "group"}}, values...)
			}
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
			switch os.Getenv("WORKIQ_PROBE_CASE") {
			case "duplicate-message":
				out["value"] = []any{map[string]any{"id": "old-first", "createdDateTime": "2026-09-10T00:00:00Z"}}
			case "exact-repeat-message": // live: Graph repeats an unchanged message on the next page
				out["value"] = append([]any{map[string]any{"id": "old-first", "createdDateTime": "2025-01-01T00:00:00Z"}}, out["value"].([]any)...)
			case "unknown-message-clock":
				out["value"] = []any{map[string]any{"id": "unknown"}, map[string]any{"id": "removed", "@removed": map[string]any{"reason": "deleted"}}}
			case "bad-message":
				out["value"] = []any{map[string]any{"createdDateTime": "2026-09-10T00:00:00Z"}}
			case "bad-message-page":
				out["value"] = "not-a-collection"
			case "bad-message-cursor":
				out["@odata.nextLink"] = "/chats/other/messages?$skiptoken=wrong"
			}
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
		if os.Getenv("WORKIQ_PROBE_CASE") == "wrong-child-parent" {
			return map[string]any{"value": []any{map[string]any{"id": id, "parentFolderId": "foreign", "childFolderCount": 0}}}, 200
		}
		// Live: the Inbox's child folders held most of a large mailbox's items.
		total, _ := strconv.Atoi(os.Getenv("WORKIQ_ACQUISITION_CHILD_TOTAL"))
		return map[string]any{"value": []any{map[string]any{"id": id, "parentFolderId": "folder-inbox", "childFolderCount": 0, "totalItemCount": total}}}, 200
	}
	if path == "/me/mailFolders/a-child/messages/delta" || path == "/me/mailFolders/a-child/messages/delta?$deltatoken=end" {
		return map[string]any{"value": []any{}, "@odata.deltaLink": "/me/mailFolders('a-child')/messages/delta?$deltatoken=end"}, 200
	}
	root := "/me/mailFolders?includeHiddenFolders=true"
	if path == root || path == root+"&$skiptoken=second" {
		switch os.Getenv("WORKIQ_PROBE_CASE") {
		case "folder-fail":
			return nil, 400
		case "bad-folder-page":
			return map[string]any{"value": "not-a-collection"}, 200
		case "bad-folder":
			return map[string]any{"value": []any{map[string]any{"id": ""}}}, 200
		case "wrong-folder-cursor":
			return map[string]any{"value": []any{}, "@odata.nextLink": "/me/messages?$skiptoken=wrong"}, 200
		}
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
		base := "/me/mailFolders/folder-" + kind + "/messages/delta"
		// The live server returns links with a parenthesized folder key, which it refuses as a request.
		link := "/me/mailFolders('folder-" + kind + "')/messages/delta"
		// "many" serves one Inbox message per delta page, as the live server's small fixed pages do.
		page := -1
		if kind == "inbox" && os.Getenv("WORKIQ_ACQUISITION_MAIL_PAGES") == "many" {
			if path == base {
				page = 0
			} else if n, err := strconv.Atoi(strings.TrimPrefix(path, base+"?$skiptoken=p")); err == nil && strings.HasPrefix(path, base+"?$skiptoken=p") {
				page, path = n, base
			}
		}
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
				}}, "@odata.deltaLink": link + "?$deltatoken=stable%2bstate"}, 200
			}
			if kind == "inbox" && strings.HasPrefix(os.Getenv("WORKIQ_ACQUISITION_MODE"), "removed") {
				values := []any{}
				if strings.Contains(path, "$deltatoken=") {
					values = append(values, map[string]any{"id": "graph-one", "@removed": map[string]any{"reason": "deleted"}})
				}
				return map[string]any{"value": values, "@odata.deltaLink": link + "?$deltatoken=stable%2bstate"}, 200
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
				// Live: WorkIQ ended mid-run once, after 840 pages, with nothing m365crawl kept.
				if die := os.Getenv("WORKIQ_ACQUISITION_DIE_ONCE"); die != "" || os.Getenv("WORKIQ_ACQUISITION_DIE_ALWAYS") != "" {
					if die == "" || createOnce(die) {
						_, _ = fmt.Fprintln(os.Stderr, `fatal: transport connection lost for user@example.invalid subject="Private plan"`)
						os.Exit(3)
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
			if kind == "inbox" && os.Getenv("WORKIQ_ACQUISITION_REMOVE_ONE") != "" && strings.Contains(path, "$deltatoken=") {
				values = []any{map[string]any{"id": "graph-1", "@removed": map[string]any{"reason": "deleted"}}}
			}
			if page >= 0 && page < len(values) {
				out := map[string]any{"value": values[page : page+1]}
				if page+1 < len(values) {
					out["@odata.nextLink"] = fmt.Sprintf("%s?$skiptoken=p%d", link, page+1)
				} else {
					out["@odata.deltaLink"] = link + "?$deltatoken=stable%2bstate"
				}
				return out, 200
			}
			return map[string]any{"value": values, "@odata.deltaLink": link + "?$deltatoken=stable%2bstate"}, 200
		}
	}
	return nil, 400
}

// refusal reproduces the live error envelopes: WorkIQ's plain-text path refusal, which echoes the
// path, and throttling whose wait is a retryAfterSeconds result field rather than a header.
func refusal(path string) map[string]any {
	failed := func(item map[string]any) map[string]any {
		return map[string]any{"isError": true, "structuredContent": map[string]any{"results": []any{item}}}
	}
	inbox := strings.HasPrefix(path, "/me/mailFolders/folder-inbox/messages/delta")
	if strings.Contains(path, "('") || inbox && os.Getenv("WORKIQ_ACQUISITION_MODE") == "refused" {
		return failed(map[string]any{"data": nil, "error": "Access denied for GET path: " + path, "statusCode": 400})
	}
	if name := os.Getenv("WORKIQ_ACQUISITION_THROTTLE_ONCE"); name != "" && inbox {
		f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return failed(map[string]any{"data": nil, "statusCode": 429, "retryAfterSeconds": 12,
				"error": map[string]any{"error": map[string]any{"code": "TooManyRequests", "message": "Too many requests. Please try again later."}}})
		}
		if !os.IsExist(err) {
			os.Exit(88)
		}
	}
	return nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println("1.0.1.47154") // WorkIQ 1.0.1's actual version output
		return
	}
	if strings.Join(os.Args[1:], " ") != "mcp" &&
		strings.Join(os.Args[1:], " ") != "mcp --account test.user@example.invalid" &&
		strings.Join(os.Args[1:], " ") != "mcp --account other@example.invalid" {
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
					URLs   []string        `json:"entityUrls"`
					Parent string          `json:"parentUrl"`
					Action string          `json:"actionUrl"`
					Body   json.RawMessage `json:"jsonBody"`
					Query  string          `json:"query"`
					Path   string          `json:"path"`
					Caps   []struct {
						Name  string              `json:"name"`
						Items []map[string]string `json:"items_by_sharepoint_ids"`
					} `json:"capabilities"`
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
			if result := documentTool(request.Params.Name, request.Params.Args.Query, request.Params.Args.Path, request.Params.Args.Caps); result != nil {
				reply["result"] = result
				break
			}
			if request.Params.Name == "create_entity" || request.Params.Name == "do_action" {
				if name := os.Getenv("WORKIQ_ACQUISITION_TRANSCRIPT"); name != "" {
					f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
					if err != nil {
						os.Exit(83)
					}
					_ = json.NewEncoder(f).Encode(map[string]any{"tool": request.Params.Name, "path": request.Params.Args.Parent + request.Params.Args.Action, "pinned": len(os.Args) == 4})
					_ = f.Close()
				}
				result, ok := createEntity(request.Params.Args.Parent, request.Params.Args.Body)
				if request.Params.Args.Action != "" {
					result, ok = doAction(request.Params.Args.Action, request.Params.Args.Body)
				}
				if !ok {
					os.Exit(93)
				}
				reply["result"] = result
				break
			}
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
			if data, status, ok := writeFetch(path); ok && os.Getenv("WORKIQ_WRITES_STATE") != "" {
				reply["result"] = map[string]any{"structuredContent": map[string]any{"results": []any{
					map[string]any{"data": data, "statusCode": status},
				}}}
				break
			}
			if result := refusal(path); result != nil {
				reply["result"] = result
				break
			}
			if die := os.Getenv("WORKIQ_ACQUISITION_DIE_PATH"); die != "" && path == die { // live: WorkIQ lost mid-traversal
				if f, err := os.Create(os.Getenv("WORKIQ_ACQUISITION_DIE_MARK")); err == nil {
					_ = f.Close()
				}
				_, _ = fmt.Fprintln(os.Stderr, "fatal: connection lost")
				os.Exit(3)
			}
			if result := failFrom(path); result != nil {
				reply["result"] = result
				break
			}
			if mb := loadMailbox(); mb != nil {
				if data, status, ok := mb.fetch(path); ok {
					result := map[string]any{"data": data, "statusCode": status}
					if status >= 400 {
						result = map[string]any{"data": nil, "statusCode": status, "error": data}
						reply["result"] = map[string]any{"isError": true, "structuredContent": map[string]any{"results": []any{result}}}
						break
					}
					reply["result"] = map[string]any{"structuredContent": map[string]any{"results": []any{result}}}
					break
				}
			}
			if data, status, ok := documentItem(path); ok {
				reply["result"] = map[string]any{"structuredContent": map[string]any{"results": []any{
					map[string]any{"data": data, "statusCode": status},
				}}}
				break
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
