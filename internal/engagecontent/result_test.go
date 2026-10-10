package engagecontent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func rawValue(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func nativeResult() scriptResult {
	account := SourceAccount{Host: "engage.cloud.microsoft", NetworkID: "network", UserID: "viewer"}
	return scriptResult{
		State: "observations", Generation: "synthetic-generation", Account: account,
		AccountEvidence: []SourceAccount{account},
		Threads: []scriptThread{{
			ID: "thread", NetworkID: "network", GroupID: "group", StarterID: "starter",
			CreatedRaw: rawValue("2026-10-10T00:00:00.000000001Z"), UpdatedRaw: rawValue("invalid native time"),
			StarterCreatedRaw: rawValue("2026-10-10T00:00:00Z"), StarterUpdatedRaw: rawValue("2026-10-10T00:01:00Z"),
			SenderID: rawValue("sender"), Language: rawValue("en"), Title: rawValue("title"),
			Version: rawValue(2), IsDeleted: rawValue(false), IsDraft: rawValue(nil),
			Blocks: []string{"initial", "", "later\n"},
		}},
	}
}

func TestIndependentNativeResultAdmissionAndOwnership(t *testing.T) {
	raw := nativeResult()
	got, err := decode(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "observations" || got.Complete || got.Qualification != "native_home_observations" || len(got.Threads) != 1 ||
		got.Threads[0].CreatedAt == nil || got.Threads[0].UpdatedAt != nil || got.Threads[0].UpdatedRaw == nil ||
		*got.Threads[0].UpdatedRaw != "invalid native time" || got.Threads[0].IsDraft != nil || got.Threads[0].IsDeleted == nil ||
		*got.Threads[0].IsDeleted || got.Threads[0].Version == nil || *got.Threads[0].Version != 2 {
		t.Fatalf("native evidence/coherence: %+v", got)
	}
	raw.Threads[0].Blocks[0] = "input mutation"
	raw.Threads[0].Title[0] = 'x'
	if got.Threads[0].Blocks[0] != "initial" || *got.Threads[0].Title != "title" {
		t.Fatal("output aliases native input")
	}
	got.Threads[0].Blocks[2] = "output mutation"
	if raw.Threads[0].Blocks[2] != "later\n" {
		t.Fatal("input aliases native output")
	}
}

func TestIndependentResultIdentityAndStateRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		mutate     func(*scriptResult)
	}{
		{"fatal", "too_large", func(r *scriptResult) { r.Fatal = "too_large" }},
		{"invented-fatal", "malformed", func(r *scriptResult) { r.Fatal = "private content" }},
		{"state", "malformed", func(r *scriptResult) { r.State = "saved_complete" }},
		{"missing-viewer", "identity_missing", func(r *scriptResult) { r.AccountEvidence = nil }},
		{"drift", "identity_drift", func(r *scriptResult) {
			r.AccountEvidence = append(r.AccountEvidence, SourceAccount{Host: r.Account.Host, UserID: "other", NetworkID: r.Account.NetworkID})
		}},
		{"account-host", "identity_missing", func(r *scriptResult) { r.Account.Host = "web.yammer.com" }},
		{"account-id", "identity_missing", func(r *scriptResult) { r.Account.UserID = "" }},
		{"required-id", "malformed", func(r *scriptResult) { r.Threads[0].StarterID = "" }},
		{"ordinal", "malformed", func(r *scriptResult) { r.Threads[0].Ordinal = 2 }},
		{"wrong-native-network", "malformed", func(r *scriptResult) { r.Threads[0].NetworkID = "foreign" }},
		{"metadata-with-content", "malformed", func(r *scriptResult) { r.State = "metadata_only" }},
		{"observations-empty", "malformed", func(r *scriptResult) { r.Threads = nil }},
		{"loss-code", "malformed", func(r *scriptResult) { r.Losses = []Loss{{Code: "private content", Count: 1}} }},
		{"loss-count", "malformed", func(r *scriptResult) { r.Losses = []Loss{{Code: "graphql_error", Count: 0}} }},
		{"invalid-utf8", "malformed", func(r *scriptResult) { r.Threads[0].Blocks = []string{"\xff"} }},
		{"nul", "malformed", func(r *scriptResult) { r.Threads[0].ID = "x\x00y" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := nativeResult()
			tc.mutate(&raw)
			got, err := decode(context.Background(), raw)
			var coded *ReadError
			if !errors.As(err, &coded) || coded.Code != tc.code || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("refusal: %+v, %v", got, err)
			}
		})
	}
}

func TestNativeUnknownVersionAndOptionalFields(t *testing.T) {
	for _, value := range []json.RawMessage{nil, rawValue(nil), rawValue(-1), rawValue(1.5), []byte("9007199254740992"), []byte("1e100000000"), []byte("1.0000000000000001"), rawValue("2"), rawValue(map[string]bool{"unmapped": true})} {
		raw := nativeResult()
		raw.Threads[0].Version = value
		raw.Threads[0].IsDeleted = rawValue("false")
		raw.Threads[0].Title = rawValue(map[string]bool{"unmapped": true})
		got, err := decode(context.Background(), raw)
		if err != nil || got.Threads[0].Version != nil || got.Threads[0].IsDeleted != nil || got.Threads[0].Title != nil || len(got.Losses) < 3 {
			t.Fatalf("unknown optional evidence: %+v, %v", got, err)
		}
	}
	for _, number := range []string{"0", "2.0", "9007199254740991"} {
		raw := nativeResult()
		raw.Threads[0].Version = []byte(number)
		got, err := decode(context.Background(), raw)
		if err != nil || got.Threads[0].Version == nil {
			t.Fatalf("exact integral version rejected: %+v, %v", got, err)
		}
	}
}

func TestIndependentViewerFragmentAgreement(t *testing.T) {
	for _, id := range []string{"viewer", "other"} {
		raw := nativeResult()
		raw.ViewerFragments = []SourceAccount{{Host: "engage.cloud.microsoft", UserID: id}}
		got, err := decode(context.Background(), raw)
		if id == "viewer" {
			if err != nil || len(got.Threads) != 1 {
				t.Fatalf("consistent fragment was promoted/refused: %+v, %v", got, err)
			}
		} else {
			var coded *ReadError
			if !errors.As(err, &coded) || coded.Code != "identity_drift" || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("conflicting fragment admitted: %+v, %v", got, err)
			}
		}
	}
}

func TestIndependentResultSelectedLimitsAndMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		mutate     func(*scriptResult)
	}{
		{"string-exact", "", func(r *scriptResult) { r.Threads[0].ID = strings.Repeat("x", 262144) }},
		{"string-plus-one", "too_large", func(r *scriptResult) { r.Threads[0].ID = strings.Repeat("x", 262145) }},
		{"text-exact", "", func(r *scriptResult) {
			r.Threads[0].Title = rawValue(nil)
			r.Threads[0].Blocks = []string{strings.Repeat("x", 262144), strings.Repeat("x", 262144), strings.Repeat("x", 262144), strings.Repeat("x", 262144)}
		}},
		{"text-plus-one", "too_large", func(r *scriptResult) {
			r.Threads[0].Title = rawValue(nil)
			r.Threads[0].Blocks = []string{strings.Repeat("x", 262144), strings.Repeat("x", 262144), strings.Repeat("x", 262144), strings.Repeat("x", 262144), "y"}
		}},
		{"blocks-exact", "", func(r *scriptResult) { r.Threads[0].Blocks = make([]string, 4096) }},
		{"blocks-plus-one", "too_large", func(r *scriptResult) { r.Threads[0].Blocks = make([]string, 4097) }},
		{"metadata", "", func(r *scriptResult) {
			r.State = "metadata_only"
			r.Threads = nil
			r.Losses = []Loss{{Code: "native_content_not_observed", Count: 1}}
		}},
		{"metadata-silent", "malformed", func(r *scriptResult) { r.State = "metadata_only"; r.Threads = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := nativeResult()
			tc.mutate(&raw)
			got, err := decode(context.Background(), raw)
			if tc.code == "" {
				if err != nil || got.Complete {
					t.Fatalf("literal bound refused: %+v, %v", got, err)
				}
			} else {
				var coded *ReadError
				if !errors.As(err, &coded) || coded.Code != tc.code || !reflect.DeepEqual(got, Result{}) {
					t.Fatalf("literal bound accepted: %+v, %v", got, err)
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := decode(ctx, nativeResult()); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("cancelled result published: %+v, %v", got, err)
	}
}
