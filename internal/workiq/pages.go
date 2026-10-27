package workiq

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/ourostack/m365crawl/internal/strictjson"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// RelativePath preserves encoded cursor bytes; scope is the caller's exact resource prefix.
func RelativePath(raw, scope string) (string, error) {
	if raw == "" || len(raw) > maxPath || strings.ContainsAny(raw, "\\\r\n\t\x00") {
		return "", failure("workiq_cursor_invalid")
	}
	path := raw
	if strings.HasPrefix(path, "https://graph.microsoft.com/v1.0/") {
		path = strings.TrimPrefix(path, "https://graph.microsoft.com/v1.0")
	}
	u, err := url.ParseRequestURI(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") ||
		u.Host != "" || u.Scheme != "" || u.Fragment != "" || strings.Contains(path, "#") ||
		strings.Contains(u.Path, "/../") || strings.Contains(u.Path, "/./") || strings.Contains(u.Path, "\\") {
		return "", failure("workiq_cursor_invalid")
	}
	if !(u.Path == "/me" || strings.HasPrefix(u.Path, "/me/") || strings.HasPrefix(u.Path, "/chats/")) {
		return "", failure("workiq_cursor_invalid")
	}
	if strings.ContainsAny(u.Path, "\r\n\t\x00") {
		return "", failure("workiq_cursor_invalid")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "", failure("workiq_cursor_invalid")
		}
	}
	if scope != "" && u.Path != scope {
		return "", failure("workiq_cursor_invalid")
	}
	return path, nil
}

type toolResult struct {
	IsError    bool `json:"isError"`
	Structured struct {
		Results []struct {
			Data   json.RawMessage `json:"data"`
			Status int             `json:"statusCode"`
			Error  json.RawMessage `json:"error"`
		} `json:"results"`
	} `json:"structuredContent"`
}

var scrubServer = teamsdesktop.Scrub

func decodePage(ctx context.Context, raw []byte) (Page, error) {
	budget := strictjson.Budget{Context: ctx, MaxDepth: 64, MaxMembers: 131072}
	if len(raw) > maxFrame {
		return Page{}, failure("workiq_page_too_large")
	}
	if _, err := budget.Parse(raw); err != nil {
		return Page{}, failure("workiq_protocol")
	}
	var before toolResult
	if json.Unmarshal(raw, &before) != nil || before.IsError {
		return Page{}, classify(raw)
	}
	if len(before.Structured.Results) != 1 {
		return Page{}, failure("workiq_protocol")
	}
	safe, _ := scrubServer(raw)
	var after toolResult
	if json.Unmarshal(safe, &after) != nil || len(after.Structured.Results) != 1 {
		return Page{}, failure("workiq_protocol")
	}
	b, a := before.Structured.Results[0], after.Structured.Results[0]
	if a.Status < 100 || a.Status > 599 {
		return Page{}, failure("workiq_protocol")
	}
	if a.Status >= 400 || len(a.Error) > 0 && string(a.Error) != "null" {
		return Page{StatusCode: a.Status}, classify(a.Error)
	}
	var oldLinks, links struct {
		Next  string `json:"@odata.nextLink"`
		Delta string `json:"@odata.deltaLink"`
	}
	if json.Unmarshal(b.Data, &oldLinks) != nil || json.Unmarshal(a.Data, &links) != nil {
		return Page{}, failure("workiq_protocol")
	}
	if oldLinks != links {
		return Page{}, failure("workiq_cursor_redacted")
	}
	for _, cursor := range []string{links.Next, links.Delta} {
		if cursor != "" {
			if _, err := RelativePath(cursor, ""); err != nil {
				return Page{}, err
			}
		}
	}
	return Page{JSON: safe, Data: a.Data, StatusCode: a.Status, NextCursor: links.Next, DeltaCursor: links.Delta}, nil
}
