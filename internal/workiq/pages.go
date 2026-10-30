package workiq

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/ourostack/m365crawl/internal/strictjson"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

const graphPrefix = "https://graph.microsoft.com/v1.0"

// mailFolderCursor is the one exception to opaque cursor bytes. Graph returns mail links with a
// leading /me/mailFolders('<id>') key segment, which WorkIQ refuses as a request; it accepts
// /me/mailFolders/<id>. Only that exact segment changes, and every other byte stays as sent.
func mailFolderCursor(raw string) string {
	prefix, rest := "", raw
	if strings.HasPrefix(rest, graphPrefix+"/") {
		prefix, rest = graphPrefix, strings.TrimPrefix(rest, graphPrefix)
	}
	const open = "/me/mailFolders('"
	if !strings.HasPrefix(rest, open) {
		return raw
	}
	id, tail, ok := strings.Cut(strings.TrimPrefix(rest, open), "')")
	if !ok || id == "" || strings.ContainsAny(id, "'/()?#") || tail != "" && tail[0] != '/' && tail[0] != '?' {
		return raw
	}
	return prefix + "/me/mailFolders/" + id + tail
}

// RelativePath preserves encoded cursor bytes; scope is the caller's exact resource prefix.
func RelativePath(raw, scope string) (string, error) {
	if raw == "" || len(raw) > maxPath || strings.ContainsAny(raw, "\\\r\n\t\x00") {
		return "", failure("workiq_cursor_invalid")
	}
	path := raw
	if strings.HasPrefix(path, graphPrefix+"/") {
		path = strings.TrimPrefix(path, graphPrefix)
	}
	u, err := url.ParseRequestURI(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") ||
		u.Host != "" || u.Scheme != "" || u.Fragment != "" || strings.Contains(path, "#") ||
		strings.Contains(u.Path, "/../") || strings.Contains(u.Path, "/./") || strings.Contains(u.Path, "\\") {
		return "", failure("workiq_cursor_invalid")
	}
	if u.Path != "/me" && !strings.HasPrefix(u.Path, "/me/") && !strings.HasPrefix(u.Path, "/chats/") {
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
			Data       json.RawMessage `json:"data"`
			Status     int             `json:"statusCode"`
			Error      json.RawMessage `json:"error"`
			RetryAfter json.RawMessage `json:"retryAfterSeconds"`
		} `json:"results"`
	} `json:"structuredContent"`
}

// failed says the envelope holds exactly one result that is a server failure.
func (r toolResult) failed() bool {
	results := r.Structured.Results
	return len(results) == 1 && (results[0].Status >= 400 || len(results[0].Error) > 0 && string(results[0].Error) != "null")
}

// retryAfter is a result's retryAfterSeconds as whole seconds, rounded up so a retry is never
// early; empty when absent or not a sane non-negative number.
func retryAfter(raw json.RawMessage) string {
	var seconds *float64
	if json.Unmarshal(raw, &seconds) != nil || seconds == nil || *seconds < 0 || *seconds > 1e9 {
		return ""
	}
	return strconv.FormatInt(int64(math.Ceil(*seconds)), 10)
}

// resultError classifies a failed result. WorkIQ's own path refusal is plain text that echoes the
// requested path, so it maps to a fixed code before any other reading of the text.
func resultError(raw json.RawMessage) error {
	var text string
	if json.Unmarshal(raw, &text) == nil && strings.HasPrefix(text, "Access denied for GET path") {
		return failure("workiq_policy_denied")
	}
	return classify(raw)
}

var scrubServer = teamsdesktop.Scrub

// DecodePage admits an entire envelope at storage boundaries as well as over stdio.
func DecodePage(ctx context.Context, raw []byte) (Page, error) { return decodePage(ctx, raw) }

func decodePage(ctx context.Context, raw []byte) (Page, error) {
	budget := strictjson.Budget{Context: ctx, MaxDepth: 64, MaxMembers: 131072}
	if len(raw) > maxFrame {
		return Page{}, failure("workiq_page_too_large")
	}
	if _, err := budget.Parse(raw); err != nil {
		return Page{}, failure("workiq_protocol")
	}
	var before toolResult
	if json.Unmarshal(raw, &before) != nil || before.IsError && !before.failed() {
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
		return Page{StatusCode: a.Status, RetryAfter: retryAfter(a.RetryAfter)}, resultError(a.Error)
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
	links.Next, links.Delta = mailFolderCursor(links.Next), mailFolderCursor(links.Delta)
	for _, cursor := range []string{links.Next, links.Delta} {
		if cursor != "" {
			if _, err := RelativePath(cursor, ""); err != nil {
				return Page{}, err
			}
		}
	}
	return Page{JSON: safe, Data: a.Data, StatusCode: a.Status, NextCursor: links.Next, DeltaCursor: links.Delta}, nil
}
