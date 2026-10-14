package engagecontent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func finalFixture(t *testing.T) Result {
	t.Helper()
	r, err := decode(context.Background(), nativeResult())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Catches acceptance of tampered final native evidence, independently of wire decoding.
func TestEngageFinalResultContract(t *testing.T) {
	for name, mutate := range map[string]func(*Result){
		"host":                 func(r *Result) { r.Account.Host = "other" },
		"account":              func(r *Result) { r.Account.UserID = "" },
		"qualification":        func(r *Result) { r.Qualification = "complete" },
		"complete":             func(r *Result) { r.Complete = true },
		"observations-empty":   func(r *Result) { r.Threads = nil },
		"metadata-threads":     func(r *Result) { r.State = "metadata_only" },
		"network":              func(r *Result) { r.Threads[0].NetworkID = "foreign" },
		"ordinal":              func(r *Result) { r.Threads[0].Ordinal = 1 },
		"utf8":                 func(r *Result) { r.Threads[0].ID = "\xff" },
		"nul":                  func(r *Result) { r.Threads[0].Blocks = []string{"\x00"} },
		"required":             func(r *Result) { r.Threads[0].StarterID = "" },
		"loss-unknown":         func(r *Result) { r.Losses = []Loss{{"unknown", 1}} },
		"loss-zero":            func(r *Result) { r.Losses = []Loss{{"clock_unmapped", 0}} },
		"loss-large":           func(r *Result) { r.Losses = []Loss{{"clock_unmapped", 65537}} },
		"loss-duplicate":       func(r *Result) { r.Losses = []Loss{{"clock_unmapped", 1}, {"clock_unmapped", 1}} },
		"loss-order":           func(r *Result) { r.Losses = []Loss{{"replies_unmapped", 1}, {"clock_unmapped", 1}} },
		"version":              func(r *Result) { n := int64(9007199254740992); r.Threads[0].Version = &n },
		"optional":             func(r *Result) { s := "\x00"; r.Threads[0].Language = &s },
		"raw":                  func(r *Result) { s := "\xff"; r.Threads[0].UpdatedRaw = &s },
		"missing-clock-loss":   func(r *Result) { r.Losses = nil },
		"missing-parsed":       func(r *Result) { r.Threads[0].CreatedAt = nil },
		"unserializable-clock": func(r *Result) { at := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC); r.Threads[0].CreatedAt = &at },
		"unserializable-offset": func(r *Result) {
			s := "2026-01-01T00:00:00+24:00"
			at, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				t.Fatal(err)
			}
			r.Threads[0].CreatedRaw, r.Threads[0].CreatedAt = &s, &at
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := finalFixture(t)
			mutate(&r)
			if err := ValidateResult(context.Background(), r); err == nil {
				t.Fatal("tampered result admitted")
			}
		})
	}
	r := finalFixture(t)
	before, _ := json.Marshal(r)
	if err := ValidateResult(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(r)
	if string(before) != string(after) {
		t.Fatal("validation mutated result")
	}
	r.State, r.Threads = "metadata_only", nil
	r.Losses = []Loss{{"native_content_not_observed", 1}}
	if err := ValidateResult(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.Losses = nil
	if err := ValidateResult(context.Background(), r); err == nil {
		t.Fatal("metadata without loss admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ValidateResult(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestEngageFinalClockContract(t *testing.T) {
	for _, raw := range []string{"0001-01-01T00:00:00Z", "9999-12-31T23:30:00-01:00", "0000-01-01T00:30:00+01:00", "2026-01-01T01:00:00.123456789+01:00", "not-a-clock"} {
		r := finalFixture(t)
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		r.Threads[0].StarterUpdatedRaw = &raw
		if err == nil {
			r.Threads[0].StarterUpdatedAt = &parsed
		} else {
			r.Threads[0].StarterUpdatedAt = nil
			r.Losses = []Loss{{"clock_unmapped", 2}}
		}
		if err := ValidateResult(context.Background(), r); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		encoded, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var copy Result
		if err := json.Unmarshal(encoded, &copy); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(r, copy) {
			t.Fatal("clock representation changed")
		}
		bad := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		r.Threads[0].StarterUpdatedAt = &bad
		if err := ValidateResult(context.Background(), r); err == nil {
			t.Fatal("contradictory clock admitted")
		}
		r.Threads[0].StarterUpdatedRaw = nil
		if err := ValidateResult(context.Background(), r); err == nil {
			t.Fatal("parsed without raw admitted")
		}
	}
}

func TestEngageFinalBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(*Result, int)
	}{
		{"threads", func(r *Result, n int) {
			th := r.Threads[0]
			r.Threads = make([]ThreadObservation, 128+n)
			for i := range r.Threads {
				r.Threads[i] = th
				r.Threads[i].Ordinal = i
			}
		}},
		{"blocks", func(r *Result, n int) { r.Threads[0].Blocks = make([]string, 4096+n) }},
		{"total-blocks", func(r *Result, n int) {
			th := r.Threads[0]
			r.Threads = make([]ThreadObservation, 17)
			for i := range r.Threads {
				r.Threads[i] = th
				r.Threads[i].Ordinal = i
				r.Threads[i].Blocks = make([]string, 4096)
				if i == 16 {
					r.Threads[i].Blocks = make([]string, n)
				}
			}
		}},
		{"string", func(r *Result, n int) { r.Threads[0].ID = strings.Repeat("i", 262144+n) }},
		{"text", func(r *Result, n int) {
			r.Threads[0].Title = nil
			r.Threads[0].Blocks = []string{strings.Repeat("x", 262144), strings.Repeat("x", 262144), strings.Repeat("x", 262144), strings.Repeat("x", 262144), strings.Repeat("x", n)}
		}},
	} {
		for n := 0; n < 2; n++ {
			t.Run(tc.name+strconv.Itoa(n), func(t *testing.T) {
				r := finalFixture(t)
				tc.make(&r, n)
				for i := range r.Losses {
					if r.Losses[i].Code == "clock_unmapped" {
						r.Losses[i].Count = len(r.Threads)
					}
				}
				err := ValidateResult(context.Background(), r)
				if (err != nil) != (n == 1) {
					t.Fatalf("boundary %d: %v", n, err)
				}
			})
		}
	}
}

func TestEngageEncodedEnvelopeBound(t *testing.T) {
	const proven = 6*8388608 + 3*65536 + 1048576
	if proven != 51576832 || proven >= MaxResultJSONBytes {
		t.Fatal("encoded envelope insufficient")
	}
	r := finalFixture(t)
	r.Threads[0].Blocks = []string{strings.Repeat("\x01", 262144)}
	r.Threads[0].Title = nil
	b, err := json.Marshal(r)
	if err != nil || len(b) > MaxResultJSONBytes {
		t.Fatal(err)
	}
	if err := ValidateResult(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestEngageFinalTotalStrings(t *testing.T) {
	for extra := 0; extra <= 1; extra++ {
		raw := nativeResult()
		raw.Threads = nil
		for i := 0; i < 8; i++ {
			n := 262144
			if i == 7 {
				n = 262018
			}
			raw.Threads = append(raw.Threads, scriptThread{Ordinal: i, ID: strings.Repeat("i", 262144), NetworkID: "network", GroupID: strings.Repeat("g", 262144), StarterID: strings.Repeat("m", 262144), SenderID: rawValue(strings.Repeat("s", n))})
		}
		r, err := decode(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		// The final model excludes the transport's second 35-byte account copy.
		s := strings.Repeat("s", 262053+extra)
		r.Threads[7].SenderID = &s
		err = ValidateResult(context.Background(), r)
		if (err != nil) != (extra == 1) {
			t.Fatalf("8MiB+%d: %v", extra, err)
		}
	}
}

func TestEngageFinalCancellationCheckpoints(t *testing.T) {
	for at := 2; at <= 8; at++ {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &checkpointContext{Context: parent, cancel: cancel, at: at}
		err := ValidateResult(ctx, finalFixture(t))
		cancel()
		if ctx.n >= at && !errors.Is(err, context.Canceled) {
			t.Fatalf("checkpoint %d: %v", at, err)
		}
	}
}
