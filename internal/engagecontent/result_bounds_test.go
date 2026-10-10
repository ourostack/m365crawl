package engagecontent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestIndependentTotalStringAndBlockLimits(t *testing.T) {
	for _, extra := range []int{0, 1} {
		raw := nativeResult()
		raw.Threads = nil
		for i := 0; i < 8; i++ {
			senderBytes := 262144
			if i == 7 {
				senderBytes = 262018 + extra
			}
			raw.Threads = append(raw.Threads, scriptThread{
				Ordinal: i, ID: strings.Repeat("i", 262144), NetworkID: "network",
				GroupID: strings.Repeat("g", 262144), StarterID: strings.Repeat("m", 262144),
				SenderID: rawValue(strings.Repeat("s", senderBytes)),
			})
		}
		got, err := decode(context.Background(), raw)
		if extra == 0 {
			if err != nil || len(got.Threads) != 8 {
				t.Fatalf("8MiB selected-string boundary refused: %+v, %v", got, err)
			}
		} else {
			var coded *ReadError
			if !errors.As(err, &coded) || coded.Code != "too_large" || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("8MiB+1 selected strings admitted: %v", err)
			}
		}
	}
	for _, count := range []int{128, 129} {
		raw := nativeResult()
		raw.Threads = make([]scriptThread, count)
		for i := range raw.Threads {
			raw.Threads[i] = scriptThread{Ordinal: i, ID: "i", NetworkID: "network", GroupID: "g", StarterID: "m"}
		}
		got, err := decode(context.Background(), raw)
		if count == 128 && (err != nil || len(got.Threads) != count) {
			t.Fatalf("128thread boundary refused: %v", err)
		}
		if count == 129 && (err == nil || !reflect.DeepEqual(got, Result{})) {
			t.Fatal("129thread bound admitted")
		}
	}
	for _, extra := range []int{0, 1} {
		raw := nativeResult()
		raw.Threads = make([]scriptThread, 16+extra)
		for i := range raw.Threads {
			blocks := 4096
			if i == 16 {
				blocks = 1
			}
			raw.Threads[i] = scriptThread{Ordinal: i, ID: "i", NetworkID: "network", GroupID: "g", StarterID: "m", Blocks: make([]string, blocks)}
		}
		got, err := decode(context.Background(), raw)
		if extra == 0 && (err != nil || len(got.Threads) != 16) {
			t.Fatalf("65,536zero-text blocks refused: %v", err)
		}
		if extra == 1 && (err == nil || !reflect.DeepEqual(got, Result{})) {
			t.Fatal("65,537zero-text blocks admitted")
		}
	}
}

func TestIndependentRawFieldErrorsAndAccountBoundaries(t *testing.T) {
	for _, mutate := range []func(*scriptResult){
		func(r *scriptResult) { r.Account.UserID = "\xff" },
		func(r *scriptResult) { r.AccountEvidence[0].UserID = "\xff" },
		func(r *scriptResult) { r.AccountEvidence = make([]SourceAccount, 129) },
		func(r *scriptResult) {
			r.ViewerFragments = []SourceAccount{{Host: "engage.cloud.microsoft", UserID: "\xff"}}
		},
		func(r *scriptResult) { r.Threads[0].CreatedRaw = rawValue(strings.Repeat("x", 262145)) },
		func(r *scriptResult) { r.Threads[0].UpdatedRaw = []byte("{") },
		func(r *scriptResult) { r.Threads[0].StarterCreatedRaw = rawValue(strings.Repeat("x", 262145)) },
		func(r *scriptResult) { r.Threads[0].StarterUpdatedRaw = []byte("{") },
		func(r *scriptResult) { r.Threads[0].SenderID = rawValue(strings.Repeat("x", 262145)) },
		func(r *scriptResult) { r.Threads[0].Language = []byte("{") },
		func(r *scriptResult) { r.Threads[0].Title = []byte("{") },
		func(r *scriptResult) { r.Threads[0].Version = []byte(strings.Repeat("1", 257)) },
		func(r *scriptResult) { r.Threads[0].IsDeleted = []byte("{") },
		func(r *scriptResult) { r.Threads[0].IsDraft = []byte("{") },
	} {
		raw := nativeResult()
		raw.ViewerFragments = []SourceAccount{{Host: "engage.cloud.microsoft", UserID: "viewer"}}
		mutate(&raw)
		if got, err := decode(context.Background(), raw); err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("malformed/large raw field published: %+v, %v", got, err)
		}
	}
	raw := nativeResult()
	raw.Threads[0].CreatedRaw = nil
	raw.Threads[0].Version = json.RawMessage("1e999999999999999999999")
	if got, err := decode(context.Background(), raw); err != nil || got.Threads[0].CreatedAt != nil || got.Threads[0].Version != nil {
		t.Fatalf("unknown clock/version rejected or invented: %+v, %v", got, err)
	}
}

type checkpointContext struct {
	context.Context
	cancel func()
	at, n  int
}

func (c *checkpointContext) Err() error {
	c.n++
	if c.n == c.at {
		c.cancel()
	}
	return c.Context.Err()
}

func TestIndependentTerminalCancellationCheckpoints(t *testing.T) {
	reached := 0
	for at := 1; at <= 12; at++ {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &checkpointContext{Context: parent, cancel: cancel, at: at}
		raw := nativeResult()
		raw.ViewerFragments = []SourceAccount{{Host: "engage.cloud.microsoft", UserID: "viewer"}}
		got, err := decode(ctx, raw)
		cancel()
		if ctx.n >= at {
			reached++
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("checkpoint%d publishes cancelled native result: %+v, %v", at, got, err)
			}
		}
	}
	if reached < 6 {
		t.Fatalf("did not exercise native publication checkpoints: %d", reached)
	}
}
