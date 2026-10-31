package workiq

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/strictjson"
)

// MaxDocumentBlob is WorkIQ fetch_blob's documented raw-file limit.
const MaxDocumentBlob = 4 << 20

// retrievalKey is the structuredContent member WorkIQ 1.0.1 retrieve answers under.
const retrievalKey = "application/vnd.ms-workiq.retrieval"

var (
	sharePointHost  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}\.sharepoint(-df)?\.com$`)
	driveIDPattern  = regexp.MustCompile(`^b![A-Za-z0-9_-]{8,256}$`)
	itemIDPattern   = regexp.MustCompile(`^[A-Za-z0-9]{8,128}$`)
	labelIDPattern  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	protectionValue = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
)

// DocumentRef names one OneDrive or SharePoint drive item.
type DocumentRef struct {
	DriveID string `json:"drive_id"`
	ItemID  string `json:"item_id"`
}

func (r DocumentRef) valid() bool {
	return driveIDPattern.MatchString(r.DriveID) && itemIDPattern.MatchString(r.ItemID)
}

func (r DocumentRef) path() string { return "/drives/" + r.DriveID + "/items/" + r.ItemID }

// DocumentItem is the drive item a link resolved to.
type DocumentItem struct {
	Ref    DocumentRef
	Name   string
	WebURL string
	Size   int64
	File   bool // the item has a file facet; folders and packages do not
}

// DocumentLabel is the sensitivity label a retrieval hit reported. Encrypted is nil when the hit
// did not say.
type DocumentLabel struct {
	ID        string
	Name      string
	Encrypted *bool
}

// RetrievalHit is one retrieve result for a file. ContentProtection is the service's own value,
// nil when absent; it is an identifier or "other", never free text.
type RetrievalHit struct {
	Ref               DocumentRef
	ContentProtection *string
	Label             *DocumentLabel
	Snippet           string
	Truncated         bool
}

// DocumentLink validates an explicit SharePoint or OneDrive link and returns the WorkIQ share
// path that resolves it. The link is used once and never stored.
func DocumentLink(raw string) (string, error) {
	if raw == "" || len(raw) > 8192 || strings.ContainsAny(raw, " \\\r\n\t\x00") {
		return "", linkUnsupported()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" ||
		!sharePointHost.MatchString(strings.ToLower(u.Hostname())) || u.Path == "" || u.Path == "/" {
		return "", linkUnsupported()
	}
	id := "u!" + strings.TrimRight(base64.URLEncoding.EncodeToString([]byte(raw)), "=")
	return "/shares/" + id + "/driveItem?$select=id,name,webUrl,size,file,parentReference", nil
}

// ResolveDocument resolves a link to exactly one file through WorkIQ fetch.
func (c *Client) ResolveDocument(ctx context.Context, link string) (DocumentItem, Page, error) {
	path, err := DocumentLink(link)
	if err != nil {
		return DocumentItem{}, Page{}, err
	}
	page, err := c.fetchPath(ctx, path)
	if err != nil {
		return DocumentItem{}, page, err
	}
	item, err := decodeDocumentItem(ctx, page.Data)
	return item, page, err
}

func decodeDocumentItem(ctx context.Context, raw json.RawMessage) (DocumentItem, error) {
	var wire struct {
		ID, Name, WebURL string
		Size             *int64
		File             json.RawMessage
		ParentReference  struct{ DriveID string }
	}
	if _, err := decodeEntity(ctx, raw, &wire); err != nil {
		return DocumentItem{}, err
	}
	item := DocumentItem{Ref: DocumentRef{DriveID: wire.ParentReference.DriveID, ItemID: wire.ID}, Name: wire.Name, WebURL: wire.WebURL}
	if wire.Size != nil {
		item.Size = *wire.Size
	}
	web, err := url.Parse(wire.WebURL)
	if !item.Ref.valid() || err != nil || web.Scheme != "https" || !sharePointHost.MatchString(strings.ToLower(web.Hostname())) ||
		wire.Name == "" || len(wire.Name) > 1024 || !utf8.ValidString(wire.Name) || item.Size < 0 {
		return DocumentItem{}, failure("workiq_protocol")
	}
	item.File = len(wire.File) > 0 && string(wire.File) != "null"
	return item, nil
}

// RetrieveDocument asks WorkIQ retrieve about exactly one file URL. Only hits for that file are
// meaningful to the caller; the rest are returned for the caller to discard.
func (c *Client) RetrieveDocument(ctx context.Context, item DocumentItem, query string) ([]RetrievalHit, error) {
	if !item.Ref.valid() || item.WebURL == "" || strings.TrimSpace(query) == "" || len(query) > 1500 {
		return nil, failure("workiq_protocol")
	}
	raw, err := c.call(ctx, requestTimeout, "tools/call", map[string]any{"name": "retrieve", "arguments": map[string]any{
		"query": query,
		"capabilities": []any{map[string]any{"name": "OneDriveAndSharePoint",
			"items_by_url": []any{map[string]any{"url": item.WebURL}}}},
	}})
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	hits, err := decodeRetrieval(ctx, raw)
	if err != nil {
		_ = c.Close()
	}
	return hits, err
}

func decodeRetrieval(ctx context.Context, raw []byte) ([]RetrievalHit, error) {
	if len(raw) > maxFrame {
		return nil, failure("workiq_page_too_large")
	}
	// Server text is scrubbed of credential-shaped values before anything reads it, as fetch pages are.
	safe, _ := scrubServer(raw)
	budget := strictjson.Budget{Context: ctx, MaxDepth: 64, MaxMembers: 131072}
	var envelope struct {
		IsError    bool                       `json:"isError"`
		Structured map[string]json.RawMessage `json:"structuredContent"`
	}
	if _, err := budget.Parse(safe); err != nil || json.Unmarshal(safe, &envelope) != nil {
		return nil, failure("workiq_protocol")
	}
	if envelope.IsError {
		return nil, classify(raw)
	}
	body, ok := envelope.Structured[retrievalKey]
	if !ok {
		return nil, failure("workiq_protocol")
	}
	var wire struct {
		Results []struct {
			DriveID           string          `json:"driveId"`
			ItemID            string          `json:"itemId"`
			ContentProtection json.RawMessage `json:"contentProtection"`
			Markdown          string          `json:"markdown"`
			Label             *struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
				IsEncrypted *bool  `json:"isEncrypted"`
			} `json:"sensitivityLabel"`
		} `json:"results"`
	}
	if json.Unmarshal(body, &wire) != nil {
		return nil, failure("workiq_protocol")
	}
	hits := make([]RetrievalHit, 0, len(wire.Results))
	for _, result := range wire.Results {
		hit := RetrievalHit{Ref: DocumentRef{DriveID: result.DriveID, ItemID: result.ItemID}}
		if len(result.ContentProtection) > 0 && string(result.ContentProtection) != "null" {
			var value string
			if json.Unmarshal(result.ContentProtection, &value) != nil || !protectionValue.MatchString(value) {
				value = "other"
			}
			hit.ContentProtection = &value
		}
		if result.Label != nil {
			if !labelIDPattern.MatchString(result.Label.ID) {
				return nil, failure("workiq_protocol")
			}
			hit.Label = &DocumentLabel{ID: strings.ToLower(result.Label.ID), Name: boundText(result.Label.DisplayName, 256), Encrypted: result.Label.IsEncrypted}
		}
		hit.Snippet, hit.Truncated = retrievalSnippet(result.Markdown)
		hits = append(hits, hit)
	}
	return hits, nil
}

// retrievalSnippet is the text of a hit's snippet bullet, the excerpt the service released, and
// whether the hit said its content was truncated.
func retrievalSnippet(markdown string) (string, bool) {
	truncated := strings.Contains(markdown, "\n  - **truncationInfo:**")
	_, rest, ok := strings.Cut(markdown, "\n  - **snippet:** ")
	if !ok {
		return "", truncated
	}
	if end := strings.Index(rest, "\n  - **"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest), truncated
}

func boundText(s string, runes int) string {
	s = strings.ToValidUTF8(s, "")
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	return string([]rune(s)[:runes])
}

// FetchDocumentBlob downloads at most MaxDocumentBlob bytes of a file through WorkIQ fetch_blob.
// The bytes are inspected for protection and discarded; they are never archived.
func (c *Client) FetchDocumentBlob(ctx context.Context, ref DocumentRef) ([]byte, int, error) {
	if !ref.valid() {
		return nil, 0, failure("workiq_protocol")
	}
	raw, err := c.call(ctx, requestTimeout, "tools/call", map[string]any{"name": "fetch_blob",
		"arguments": map[string]any{"path": ref.path() + "/content"}})
	if err != nil {
		_ = c.Close()
		return nil, 0, err
	}
	data, status, err := decodeBlob(ctx, raw)
	if err != nil && status == 0 {
		_ = c.Close()
	}
	return data, status, err
}

func decodeBlob(ctx context.Context, raw []byte) ([]byte, int, error) {
	budget := strictjson.Budget{Context: ctx, MaxDepth: 16, MaxMembers: 4096}
	var envelope struct {
		IsError    bool `json:"isError"`
		Structured struct {
			Base64 *string `json:"base64Content"`
			Size   *int64  `json:"sizeBytes"`
			Status int     `json:"statusCode"`
		} `json:"structuredContent"`
	}
	if len(raw) > maxFrame {
		return nil, 0, failure("workiq_page_too_large")
	}
	if _, err := budget.Parse(raw); err != nil || json.Unmarshal(raw, &envelope) != nil {
		return nil, 0, failure("workiq_protocol")
	}
	status := envelope.Structured.Status
	if status >= 400 && status <= 599 {
		return nil, status, failure("workiq_failed")
	}
	if envelope.IsError {
		return nil, 0, classify(raw)
	}
	if status != 200 || envelope.Structured.Base64 == nil || envelope.Structured.Size == nil {
		return nil, 0, failure("workiq_protocol")
	}
	data, err := base64.StdEncoding.DecodeString(*envelope.Structured.Base64)
	if err != nil || int64(len(data)) != *envelope.Structured.Size || len(data) > MaxDocumentBlob {
		return nil, 0, failure("workiq_protocol")
	}
	return data, status, nil
}

// Protection states of a file's bytes.
const (
	BytesNotEncrypted = "not_encrypted"
	BytesEncrypted    = "encrypted"
	BytesUnknown      = "unknown"
)

var (
	cfbSignature     = []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}
	encryptedPackage = utf16le("EncryptedPackage")
)

func utf16le(s string) []byte {
	out := make([]byte, 0, 2*len(s))
	for _, c := range []byte(s) {
		out = append(out, c, 0)
	}
	return out
}

// InspectOffice classifies Office bytes without decrypting them: an OOXML zip is not encrypted, a
// compound file holding an EncryptedPackage stream is, and anything else is unknown.
func InspectOffice(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return BytesNotEncrypted
	case bytes.HasPrefix(data, cfbSignature) && bytes.Contains(data, encryptedPackage):
		return BytesEncrypted
	}
	return BytesUnknown
}

func linkUnsupported() *errs.Coded {
	return &errs.Coded{Code: "document_link_unsupported", Exit: errs.ExitUsage,
		Message: "unsupported document link",
		Fix:     "Pass one HTTPS tenant.sharepoint.com or tenant-my.sharepoint.com document link: a sharing link, a Doc.aspx link or the file's own URL, such as the href of a file attached to a Teams message."}
}
