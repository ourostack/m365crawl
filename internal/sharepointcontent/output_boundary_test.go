package sharepointcontent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIndependentOutputRejectsMalformedFields(t *testing.T) {
	req := admittedRequest{Kind: "page", Host: "fixture.sharepoint.com", Site: "/sites/sample", Path: "/sites/sample/SitePages/Page.aspx"}
	for _, test := range []struct {
		name   string
		mutate func(*scriptResult)
		code   string
	}{
		{"unknown", func(r *scriptResult) { r.State = "unknown" }, "malformed"},
		{"missing-status", func(r *scriptResult) { r.HTTPStatus = 0 }, "malformed"},
		{"invalid-string", func(r *scriptResult) { r.Account.LoginName = "\xff" }, "malformed"},
		{"null-char", func(r *scriptResult) { r.File.Title = stringPointer("x\x00y") }, "malformed"},
		{"bad-list-id", func(r *scriptResult) { v := int64(0); r.File.ListItemID = &v }, "malformed"},
		{"bad-length", func(r *scriptResult) { v := int64(-1); r.File.Length = &v }, "malformed"},
		{"duplicate-loss", func(r *scriptResult) {
			r.Losses = []Loss{{Code: "modified_time_unmapped", Count: 1}, {Code: "modified_time_unmapped", Count: 1}}
		}, "malformed"},
		{"bad-loss-count", func(r *scriptResult) { r.Losses = []Loss{{Code: "modified_time_unmapped", Count: 2}} }, "malformed"},
		{"zero-loss", func(r *scriptResult) { r.Losses = []Loss{{Code: "control_id_unmapped", Count: 0}} }, "malformed"},
		{"metadata-controls", func(r *scriptResult) { r.PageControls = []PageControl{{}} }, "malformed"},
		{"too-many-controls", func(r *scriptResult) { r.State = "page_observations"; r.PageControls = make([]PageControl, 4097) }, "too_large"},
		{"bad-control-id", func(r *scriptResult) {
			r.State = "page_observations"
			r.PageControls = []PageControl{{ID: stringPointer("wrong"), State: "unmapped"}}
		}, "malformed"},
		{"bad-control-ordinal", func(r *scriptResult) {
			r.State = "page_observations"
			r.PageControls = []PageControl{{Ordinal: 1, State: "unmapped"}}
		}, "malformed"},
		{"text-without-type", func(r *scriptResult) {
			r.State = "page_observations"
			r.PageControls = []PageControl{{State: "text", Texts: []string{"text"}}}
		}, "malformed"},
		{"layout-without-type", func(r *scriptResult) {
			r.State = "page_observations"
			r.PageControls = []PageControl{{State: "layout"}}
		}, "malformed"},
		{"omitted-without-type", func(r *scriptResult) {
			r.State = "page_observations"
			r.PageControls = []PageControl{{State: "omitted"}}
		}, "malformed"},
		{"unmapped-with-text", func(r *scriptResult) {
			r.State = "page_observations"
			r.PageControls = []PageControl{{State: "unmapped", Texts: []string{"text"}}}
		}, "malformed"},
		{"unknown-control", func(r *scriptResult) {
			r.State = "page_observations"
			r.PageControls = []PageControl{{State: "unknown"}}
		}, "malformed"},
		{"bad-text", func(r *scriptResult) {
			kind := int64(4)
			r.State = "page_observations"
			r.PageControls = []PageControl{{Type: &kind, State: "text", Texts: []string{"\xff"}}}
		}, "malformed"},
		{"oversized-text", func(r *scriptResult) {
			kind := int64(4)
			r.State = "page_observations"
			r.PageControls = []PageControl{{Type: &kind, State: "text", Texts: []string{strings.Repeat("x", 262145)}}}
		}, "too_large"},
		{"aggregate-text", func(r *scriptResult) {
			kind := int64(4)
			r.State = "page_observations"
			r.PageControls = []PageControl{{Type: &kind, State: "text", Texts: []string{strings.Repeat("x", 262144), strings.Repeat("x", 262144), strings.Repeat("x", 262144), strings.Repeat("x", 262144), "x"}}}
		}, "too_large"},
		{"unexpected-transcript", func(r *scriptResult) { r.Transcript = &Transcript{ID: "transcript"} }, "malformed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := nativeResult()
			test.mutate(&raw)
			got, err := decode(context.Background(), req, raw)
			var safe *ReadError
			if !errors.As(err, &safe) || safe.Code != test.code || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("malformed output admitted: %#v,%v", got, err)
			}
		})
	}
}

func TestIndependentTranscriptMalformedAndTotalStringBounds(t *testing.T) {
	req := admittedRequest{Kind: "stream", Host: "fixture.sharepoint.com", Site: "/sites/sample", Path: "/sites/sample/SitePages/Page.aspx"}
	for _, test := range []struct {
		name   string
		mutate func(*scriptResult, *admittedRequest)
		code   string
	}{
		{"wrong-kind", func(r *scriptResult, q *admittedRequest) { q.Kind = "page" }, "malformed"},
		{"absent-transcript", func(r *scriptResult, q *admittedRequest) { r.Transcript = nil }, "malformed"},
		{"too-many", func(r *scriptResult, q *admittedRequest) { r.Transcript.Entries = make([]TranscriptEntry, 16385) }, "too_large"},
		{"wrong-selected-id", func(r *scriptResult, q *admittedRequest) { q.TranscriptID = "another" }, "identity_mismatch"},
		{"oversized-id", func(r *scriptResult, q *admittedRequest) { r.Transcript.ID = strings.Repeat("x", 262145) }, "too_large"},
		{"bad-id", func(r *scriptResult, q *admittedRequest) { r.Transcript.ID = "\xff" }, "malformed"},
		{"entry-ordinal", func(r *scriptResult, q *admittedRequest) { r.Transcript.Entries[0].Ordinal = 1 }, "malformed"},
		{"bad-entry-string", func(r *scriptResult, q *admittedRequest) { r.Transcript.Entries[0].Text = "\xff" }, "malformed"},
		{"total-strings", func(r *scriptResult, q *admittedRequest) {
			for i := 0; i < 33; i++ {
				r.Transcript.Entries = append(r.Transcript.Entries, TranscriptEntry{Ordinal: i + 1, ID: "entry", StartRaw: stringPointer(strings.Repeat("x", 262144))})
			}
		}, "too_large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := nativeResult()
			raw.State = "transcript_observations"
			raw.Transcript = &Transcript{ID: "transcript", Entries: []TranscriptEntry{{ID: "entry", Text: "text"}}}
			q := req
			test.mutate(&raw, &q)
			got, err := decode(context.Background(), q, raw)
			var safe *ReadError
			if !errors.As(err, &safe) || safe.Code != test.code || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("malformed transcript admitted: %#v,%v", got, err)
			}
		})
	}
}

type publicationContext struct {
	context.Context
	cancel context.CancelFunc
	at     int64
	checks atomic.Int64
}

func (c *publicationContext) Err() error {
	if c.checks.Add(1) == c.at {
		c.cancel()
	}
	return c.Context.Err()
}

func TestDecodeCancellationAtPublicationBoundaries(t *testing.T) {
	req := admittedRequest{Kind: "page", Host: "fixture.sharepoint.com", Path: "/sites/sample/SitePages/Page.aspx"}
	for at := int64(1); at < 10; at++ {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &publicationContext{Context: parent, cancel: cancel, at: at}
		got, err := decode(ctx, req, nativeResult())
		cancelled := parent.Err() != nil
		cancel()
		if !cancelled {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("cancelled publication leaked: %#v,%v", got, err)
		}
	}
}
