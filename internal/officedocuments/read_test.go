package officedocuments

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadJSONSourceShapes(t *testing.T) {
	zero, unpinned, sharingType := int64(0), false, int64(7)
	for _, tt := range []struct {
		surface Surface
		json    string
		want    Document
	}{
		{Recent, `{"documents":{"items":[{
 "title":"Design","url":"https://example.invalid/doc","extension":"docx","file_size":0,
 "is_pinned":false,"resource_id":"raw-resource","time_stamp":"2026-10-09T12:34:56Z",
 "onedrive_info":{"drive_id":"drive","item_id":"item"},
 "sharepoint_info":{"tenant_id":"tenant","site_id":"site","web_id":"web","list_id":"list",
 "list_item_unique_id":"unique","site_url":"https://example.invalid/site",
 "site_info":{"title":"Site"},"teams_channel_info":{"url":"https://example.invalid/channel","title":"Channel"}},
 "creation_info":{"timestamp":"2026-10-08T00:00:00Z","user_info":{"upn":"creator@example.invalid","display_name":"Creator"}},
 "modification_info":{"timestamp":"2026-10-09T00:00:00Z","user_info":{"upn":"modifier@example.invalid","display_name":"Modifier"}},
 "activity_info":{"badge":{"message_format":"{0} edited","users":[{"upn":"actor@example.invalid","display_name":"Actor"}],
 "timestamp":"2026-10-09T01:00:00Z"}}}]}}`,
			Document{Surface: Recent, Title: "Design", URL: "https://example.invalid/doc", Extension: "docx",
				Size: &zero, Pinned: &unpinned, ResourceID: "raw-resource", DriveID: "drive", ItemID: "item",
				SharePoint: SharePoint{TenantID: "tenant", SiteID: "site", WebID: "web", ListID: "list", UniqueID: "unique"},
				SiteURL:    "https://example.invalid/site", SiteTitle: "Site",
				TeamsChannelURL: "https://example.invalid/channel", TeamsChannelTitle: "Channel",
				Creator:  Person{UPN: "creator@example.invalid", DisplayName: "Creator"},
				Modifier: Person{UPN: "modifier@example.invalid", DisplayName: "Modifier"},
				Opened:   Timestamp{Raw: "2026-10-09T12:34:56Z", Value: time.Date(2026, 10, 9, 12, 34, 56, 0, time.UTC)},
				Created:  Timestamp{Raw: "2026-10-08T00:00:00Z", Value: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)},
				Modified: Timestamp{Raw: "2026-10-09T00:00:00Z", Value: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)},
				Activity: &Activity{MessageFormat: "{0} edited", Users: []Person{{UPN: "actor@example.invalid", DisplayName: "Actor"}},
					At: Timestamp{Raw: "2026-10-09T01:00:00Z", Value: time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)}}}},
		{Shared, `{"shared_documents":[{"FileName":"Shared","DocumentUrl":"https://example.invalid/shared",
 "WebUrl":"https://example.invalid/web","ResourceId":"resource","DriveId":"drive","ItemId":"item",
 "SharepointIds":{"SiteId":"site","WebId":"web","ListId":"list","ListItemId":"41","ListItemUniqueId":"unique"},
 "SharedByUserName":"Sharer","SharedByUserEmail":"sharer@example.invalid","SharingType":7,
 "SharedDate":"2026-10-08T00:00:00Z","LastModifiedDate":"2026-10-09T00:00:00Z"}]}`,
			Document{Surface: Shared, Title: "Shared", URL: "https://example.invalid/shared", WebURL: "https://example.invalid/web",
				ResourceID: "resource", DriveID: "drive", ItemID: "item",
				SharePoint: SharePoint{SiteID: "site", WebID: "web", ListID: "list", ListItemID: "41", UniqueID: "unique"},
				Modified:   Timestamp{Raw: "2026-10-09T00:00:00Z", Value: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)},
				Sharing: &Sharing{DisplayName: "Sharer", Email: "sharer@example.invalid", Type: &sharingType,
					At: Timestamp{Raw: "2026-10-08T00:00:00Z", Value: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}}}},
		{Dialog, `[{"FileName":"Local","DocumentUrl":"file:///fictional.docx","Path":"Friendly",
 "Timestamp":"2026-10-09T00:00:00Z","ResourceId":null,"IsPinned":false}]`,
			Document{Surface: Dialog, Title: "Local", URL: "file:///fictional.docx", FriendlyPath: "Friendly", Pinned: &unpinned,
				Opened: Timestamp{Raw: "2026-10-09T00:00:00Z", Value: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)}}},
		{Recommended, `{"documents_group":{"documents":[{"title":"Recommended","url":"https://example.invalid/recommended",
 "creation_info":{"user_info":{}},"modification_info":{"user_info":{}}}]}}`,
			Document{Surface: Recommended, Title: "Recommended", URL: "https://example.invalid/recommended"}},
	} {
		t.Run(string(tt.surface), func(t *testing.T) {
			got, err := ReadJSON(context.Background(), strings.NewReader(tt.json), tt.surface)
			if err != nil || !reflect.DeepEqual(got.Documents, []Document{tt.want}) || len(got.Losses) != 0 {
				t.Fatalf("source observations = %#v, %v; want %#v", got, err, tt.want)
			}
		})
	}
}

func TestReadJSONValidEmptyLists(t *testing.T) {
	for _, tt := range []struct {
		surface Surface
		json    string
	}{
		{Recent, `{"documents":{"items":[]}}`},
		{Shared, `{"shared_documents":[]}`},
		{Recommended, `{"documents_group":{"documents":[]}}`},
		{Dialog, `[]`},
	} {
		got, err := ReadJSON(context.Background(), strings.NewReader(tt.json), tt.surface)
		if err != nil || len(got.Documents) != 0 || len(got.Losses) != 0 {
			t.Fatalf("valid empty source %s = %#v, %v", tt.surface, got, err)
		}
	}
}

func TestReadJSONMissingEnvelopeRefusesAll(t *testing.T) {
	for _, tt := range []struct {
		surface Surface
		json    string
	}{
		{Recent, `{}`}, {Recent, `[]`}, {Recent, `{"documents":{}}`}, {Recent, `{"documents":{"items":null}}`},
		{Shared, `{}`}, {Shared, `{"shared_documents":{}}`},
		{Recommended, `{"documents_group":null}`}, {Dialog, `{}`}, {"unknown", `[]`},
	} {
		got, err := ReadJSON(context.Background(), strings.NewReader(tt.json), tt.surface)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("missing/unsupported source published: %#v, %v", got, err)
		}
	}
}

func TestReadJSONAmbiguousOrTrailingSourceRefusesAll(t *testing.T) {
	for _, raw := range []string{
		`[{"FileName":"A","DocumentUrl":"https://example.invalid/a","DocumentUrl":"https://example.invalid/b"}]`,
		`[{"FileName":"A","DocumentUrl":"https://example.invalid/a","extra":{"id":"a","id":"b"}}]`,
		`[] []`,
		`[{"FileName":"A","DocumentUrl":"\ud800"}]`,
		`[{"FileName":"A","DocumentUrl":"\udc00"}]`,
		"[\xff]",
	} {
		got, err := ReadJSON(context.Background(), strings.NewReader(raw), Dialog)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("ambiguous source published: %#v, %v", got, err)
		}
	}
}

func TestReadJSONOptionalKnownValuesAndMalformedTypes(t *testing.T) {
	raw := `[
 {"FileName":"bad","DocumentUrl":123},
 {"FileName":"bad","DocumentUrl":"u","IsPinned":0},
 {"FileName":"Repeated","DocumentUrl":"u","ResourceId":null,"IsPinned":false},
 {"FileName":"Repeated","DocumentUrl":"u","ResourceId":"r","IsPinned":null}
]`
	got, err := ReadJSON(context.Background(), strings.NewReader(raw), Dialog)
	unpinned := false
	want := []Document{
		{Ordinal: 2, Surface: Dialog, Title: "Repeated", URL: "u", Pinned: &unpinned},
		{Ordinal: 3, Surface: Dialog, Title: "Repeated", URL: "u", ResourceID: "r"},
	}
	if err != nil || !reflect.DeepEqual(got.Documents, want) ||
		!reflect.DeepEqual(got.Losses, []Loss{{Code: "office_document_unmapped", Count: 2}}) {
		t.Fatalf("dynamic/unknown/repeated evidence = %#v, %v", got, err)
	}
}

func TestReadJSONPreservesInvalidStatedTimestamp(t *testing.T) {
	for _, raw := range []string{"not-a-time", "2026-10-09T1:34:56Z", "2026-10-09T12:34:56,1Z",
		"2026-10-09T12:34:56+24:00", "2026-10-09T12:34:56+00:60"} {
		got, err := ReadJSON(context.Background(), strings.NewReader(
			`[{"FileName":"A","DocumentUrl":"u","Timestamp":"`+raw+`"}]`), Dialog)
		if err != nil || len(got.Documents) != 1 || got.Documents[0].Opened.Raw != raw ||
			!got.Documents[0].Opened.Value.IsZero() ||
			!reflect.DeepEqual(got.Losses, []Loss{{Code: "office_timestamp_unknown", Count: 1}}) {
			t.Fatalf("invalid stated time substituted: %#v, %v", got, err)
		}
	}
}

func TestReadJSONBoundsRefuseAll(t *testing.T) {
	raw := `[{"FileName":"a","DocumentUrl":"u"},{"FileName":"b","DocumentUrl":"v"}]`
	base := readLimits{inputBytes: int64(len(raw)), fieldBytes: 1, stringBytes: 4, rows: 2, depth: 2, members: 6}
	got, err := readJSON(context.Background(), strings.NewReader(raw), Dialog, base)
	if err != nil || len(got.Documents) != 2 {
		t.Fatalf("inclusive limits refused: %#v, %v", got, err)
	}
	for _, name := range []string{"input", "field", "strings", "rows", "depth", "members"} {
		limits := base
		switch name {
		case "input":
			limits.inputBytes--
		case "field":
			limits.fieldBytes = 0
		case "strings":
			limits.stringBytes--
		case "rows":
			limits.rows--
		case "depth":
			limits.depth--
		case "members":
			limits.members--
		}
		got, err := readJSON(context.Background(), strings.NewReader(raw), Dialog, limits)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("%s limit published partial source: %#v, %v", name, got, err)
		}
	}
}

type failedInput struct{}

func (failedInput) Read(p []byte) (int, error) {
	return copy(p, `[]`), errors.New("private-source-value")
}

type cancellingInput struct{ cancel context.CancelFunc }

func (r cancellingInput) Read(p []byte) (int, error) {
	r.cancel()
	return copy(p, `[]`), io.EOF
}

func TestReadJSONFatalInputAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, source := range []io.Reader{nil, failedInput{}, cancellingInput{cancel: cancel}} {
		got, err := ReadJSON(ctx, source, Dialog)
		if err == nil || !reflect.DeepEqual(got, Result{}) || strings.Contains(err.Error(), "private-source-value") {
			t.Fatalf("unsafe input failure: %#v, %v", got, err)
		}
	}
	got, err := ReadJSON(ctx, strings.NewReader("[]"), Dialog)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("cancelled source published: %#v, %v", got, err)
	}
}

func TestReadJSONSharingStateRemainsNativeEvidence(t *testing.T) {
	got, err := ReadJSON(context.Background(), strings.NewReader(
		`{"documents":{"items":[{"title":"A","url":"u","sharing_info":{"state":3}}]}}`), Recent)
	if err != nil || len(got.Documents) != 1 || got.Documents[0].SharingState == nil ||
		*got.Documents[0].SharingState != 3 || got.Documents[0].Sharing != nil {
		t.Fatalf("native sharing state dropped or conflated with a share event: %#v, %v", got, err)
	}
}

func TestReadJSONMalformedNestedRowsHaveExplicitLoss(t *testing.T) {
	for _, extra := range []string{
		`"onedrive_info":[]`, `"file_size":"7"`, `"file_size":-1`, `"file_size":1.5`,
		`"file_size":9223372036854775808`, `"resource_id":"bad\u0000id"`,
		`"activity_info":{"badge":{"users":{}}}`, `"activity_info":{"badge":{"users":[7]}}`,
		`"creation_info":{"user_info":[]}`, `"time_stamp":3`,
	} {
		raw := `{"documents":{"items":[{"title":"A","url":"u",` + extra + `}]}}`
		got, err := ReadJSON(context.Background(), strings.NewReader(raw), Recent)
		if err != nil || len(got.Documents) != 0 ||
			!reflect.DeepEqual(got.Losses, []Loss{{Code: "office_document_unmapped", Count: 1}}) {
			t.Fatalf("malformed nested field = %#v, %v", got, err)
		}
	}
	got, err := ReadJSON(context.Background(), strings.NewReader(`[null,7,{"FileName":"","DocumentUrl":"u"}]`), Dialog)
	if err != nil || len(got.Documents) != 0 ||
		!reflect.DeepEqual(got.Losses, []Loss{{Code: "office_document_unmapped", Count: 3}}) {
		t.Fatalf("malformed rows/identity = %#v, %v", got, err)
	}
}

func TestReadJSONUnicodeAndEscapesRetainNativeText(t *testing.T) {
	got, err := ReadJSON(context.Background(), strings.NewReader(
		`[{"FileName":"Hello 世界 \ud83d\ude00 \"quoted\" \\path","DocumentUrl":"u"}]`), Dialog)
	if err != nil || len(got.Documents) != 1 || got.Documents[0].Title != "Hello 世界 😀 \"quoted\" \\path" {
		t.Fatalf("native Unicode or escaped text changed: %#v, %v", got, err)
	}
}

func TestReadJSONMalformedSyntaxRefusesAll(t *testing.T) {
	for _, raw := range []string{
		"", `[`, `[1`, `{"`, `{"a":`, `{"a":1`, `{"a":1]`, `[1}`, `[{"FileName":"A","DocumentUrl":"u"},`,
		`[{"FileName":"\u12","DocumentUrl":"u"}]`,
		`[{"FileName":"\uZZZZ","DocumentUrl":"u"}]`,
		`[{"FileName":"\ud800\u0000","DocumentUrl":"u"}]`,
		`[{"FileName":"\ud800\uZZZZ","DocumentUrl":"u"}]`,
		`[{"FileName":"\ud800","DocumentUrl":"u"}]`,
		"[\"bad\\", "[\"\\u12",
	} {
		got, err := ReadJSON(context.Background(), strings.NewReader(raw), Dialog)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("malformed syntax retained source: %#v, %v", got, err)
		}
	}

}

type boundaryContext struct {
	context.Context
	cancel     context.CancelFunc
	checks, at int
}

func (c *boundaryContext) Err() error {
	c.checks++
	if c.checks == c.at {
		c.cancel()
	}
	return c.Context.Err()
}

func TestReadJSONCancellationAtEveryCheckedBoundary(t *testing.T) {
	raw := `[{"FileName":"a","DocumentUrl":"u"},{"FileName":"b","DocumentUrl":"v"}]`
	for at := 1; at < 100; at++ {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &boundaryContext{Context: parent, cancel: cancel, at: at}
		got, err := ReadJSON(ctx, strings.NewReader(raw), Dialog)
		cancelled := parent.Err() != nil
		cancel()
		if !cancelled {
			if err != nil || len(got.Documents) != 2 {
				t.Fatalf("uncancelled source = %#v, %v", got, err)
			}
			break
		}
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("cancelled source published earlier observations: %#v, %v", got, err)
		}
	}
}

func TestContextInputDoesNotReadAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := strings.NewReader("private-input")
	n, err := (contextInput{ctx: ctx, source: source}).Read(make([]byte, 32))
	if n != 0 || !errors.Is(err, context.Canceled) || source.Len() != 13 {
		t.Fatalf("cancelled input advanced source: n=%d, error=%v, remaining=%d", n, err, source.Len())
	}
}
