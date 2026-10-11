package teamsdesktop

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAppRawAdmissionPreservesNativeInt64AndRejectsAmbiguity(t *testing.T) {
	raw := []byte(`{"properties":{"botCitations":[{"id":9223372036854775807,"title":"Exact"}]}}`)
	projected, err := ProjectApplication(context.Background(), "message", "sender", raw, true)
	if err != nil || projected.State != "mapped" || len(projected.Observation.Citations) != 1 ||
		projected.Observation.Citations[0].ID != 9223372036854775807 {
		t.Fatalf("native integer identity changed: %+v %v", projected, err)
	}
	for _, raw := range []string{`[]`, `{} {}`, `{"properties":{},"properties":{"fromAppMetadata":{"id":"other"}}}`,
		`{"properties":{"fromAppMetadata":{"id":"\ud800"}}}`, `{"x":"` + string([]byte{0xff}) + `"}`} {
		projected, err := ProjectApplication(context.Background(), "message", "sender", []byte(raw), true)
		if err != nil || projected.State != "failed" || projected.FailureCode != "teams_app_raw_invalid" {
			t.Errorf("invalid raw became clean evidence: %+v %v", projected, err)
		}
	}
}

func TestApplicationPresenceStates(t *testing.T) {
	for _, tc := range []struct {
		name, raw, sender, state, failure string
		present                           bool
	}{
		{"null-raw", "", "sender", "failed", "teams_app_raw_missing", false},
		{"empty-raw", "", "sender", "failed", "teams_app_raw_invalid", true},
		{"absent", `{}`, "sender", "absent", "", true},
		{"absent-sender", `{"properties":null}`, "", "absent", "", true},
		{"empty-fields", `{"properties":{"fromAppMetadata":{},"botMetadata":{},"botCitations":[]}}`, "", "absent", "", true},
		{"unselected-fields", `{"properties":{"unrelated":"ignored"}}`, "", "absent", "", true},
		{"string-properties", `{"properties":"opaque"}`, "sender", "partial", "", true},
		{"metadata", `{"properties":{"fromAppMetadata":{"id":"app","name":"Native"}}}`, "sender", "mapped", "", true},
		{"empty-string-claim", `{"properties":{"fromAppMetadata":{"id":""}}}`, "sender", "mapped", "", true},
		{"reply", `{"properties":{"botMetadata":{"replyToId":"reply"}}}`, "sender", "mapped", "", true},
		{"malformed", `{"properties":{"fromAppMetadata":"opaque"}}`, "sender", "partial", "", true},
		{"malformed-citations", `{"properties":{"botCitations":{}}}`, "sender", "partial", "", true},
		{"refused-citation", `{"properties":{"botCitations":[{"id":"1","title":"Unsupported"}]}}`, "sender", "partial", "", true},
		{"nested-content", `{"properties":{"botCitations":[{"id":1,"title":"Kept","content":{}}]}}`, "sender", "partial", "", true},
		{"possible-without-sender", `{"properties":{"fromAppMetadata":{"id":"app"}}}`, "", "failed", "teams_app_identity_unqualified", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProjectApplication(context.Background(), "message", tc.sender, []byte(tc.raw), tc.present)
			if err != nil || got.State != tc.state || got.FailureCode != tc.failure {
				t.Fatalf("wrong presence meaning: %+v %v", got, err)
			}
			if tc.state == "partial" && len(got.Losses) == 0 {
				t.Fatal("partial interpretation lost native losses")
			}
			if tc.name == "empty-string-claim" && (got.Observation.AppID == nil || *got.Observation.AppID != "") {
				t.Fatal("explicit empty claim collapsed into absence")
			}
			if tc.name == "nested-content" && (len(got.Observation.Citations) != 1 || !got.Observation.Citations[0].NestedContentPresent) {
				t.Fatal("nested content presence became content reconstruction or refusal")
			}
		})
	}
}

func TestApplicationWholeRecordAndCancellationBoundaries(t *testing.T) {
	prefix, suffix := `{"body":"`, `"}`
	for _, size := range []int{8 << 20, (8 << 20) + 1} {
		raw := prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
		got, err := ProjectApplication(context.Background(), "message", "sender", []byte(raw), true)
		if err != nil || (size == 8<<20 && got.State != "absent") ||
			(size > 8<<20 && (got.State != "failed" || got.FailureCode != "teams_app_raw_too_large")) {
			t.Fatalf("whole-record boundary %d: %+v %v", size, got, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, raw := range [][]byte{nil, []byte(`{}`), []byte(`invalid`)} {
		got, err := ProjectApplication(ctx, "message", "sender", raw, true)
		if !errors.Is(err, context.Canceled) || got.State != "" {
			t.Fatalf("cancellation became successful per-record failure: %+v %v", got, err)
		}
	}
}

func TestApplicationIdentityAndNativeMapperBudgetFailures(t *testing.T) {
	for _, test := range []struct {
		ctx               context.Context
		identity, failure string
	}{
		{nil, "message", "teams_app_raw_invalid"},
		{context.Background(), "", "teams_app_identity_unqualified"},
		{context.Background(), string([]byte{0xff}), "teams_app_identity_unqualified"},
	} {
		got, err := ProjectApplication(test.ctx, test.identity, "sender", []byte(`{}`), true)
		if err != nil || got.FailureCode != test.failure {
			t.Fatal("unqualified context or owning message became clean absence")
		}
	}
	for _, raw := range []string{
		`{"properties":{"fromAppMetadata":{"id":"` + strings.Repeat("x", (256<<10)+1) + `"}}}`,
		`{"properties":{"botCitations":[` + strings.Repeat(`{"id":1},`, 4096) + `{"id":1}]}}`,
		`{"properties":{"botCitations":[` + strings.Repeat(`{"id":1,"title":"`+strings.Repeat("x", 200<<10)+`"},`, 21) + `{"id":1,"title":"` + strings.Repeat("x", 200<<10) + `"}]}}`,
	} {
		got, err := ProjectApplication(context.Background(), "message", "sender", []byte(raw), true)
		if err != nil || got.State != "failed" || got.FailureCode != "teams_bot_input_too_large" || len(got.Observation.Citations) != 0 {
			t.Fatal("native mapper refusal leaked partial observation or changed fixed failure code")
		}
	}
	for _, raw := range []string{
		`{"x":` + strings.Repeat("[", 64) + `0` + strings.Repeat("]", 64) + `}`,
		`{"x":[` + strings.Repeat("0,", 131072) + `0]}`,
	} {
		got, err := ProjectApplication(context.Background(), "message", "sender", []byte(raw), true)
		if err != nil || got.FailureCode != "teams_app_raw_too_large" {
			t.Fatal("whole record structural budget was bypassed by application absence")
		}
	}
}

type applicationCancelContext struct {
	context.Context
	cancel       context.CancelFunc
	calls, after int
}

func (c *applicationCancelContext) Err() error {
	c.calls++
	if c.calls >= c.after {
		c.cancel()
	}
	return c.Context.Err()
}

func TestApplicationCancellationDuringNativeMappingDisposesObservation(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &applicationCancelContext{Context: base, cancel: cancel, after: 6}
	got, err := ProjectApplication(ctx, "message", "sender", []byte(`{"properties":{"fromAppMetadata":{"id":"app"}}}`), true)
	if !errors.Is(err, context.Canceled) || got.State != "" || got.Observation.AppID != nil {
		t.Fatal("mid-mapping cancellation became per-record failure or leaked observation")
	}
}
