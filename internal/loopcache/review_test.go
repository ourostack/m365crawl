package loopcache

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestReadSnapshotLegacyVersionOneCannotRefundWork(t *testing.T) {
	for _, other := range []any{nil, []any{}, "malformed"} {
		header, body := legacyChunks()
		header["version"] = "1"
		header["segmentTexts"] = make([]any, 65537)
		if other != nil {
			header["segments"] = other
		}
		got, err := ReadSnapshot(context.Background(), channelFixture(t, header, body, nil, nil, 10))
		var typed *ReadError
		if !errors.As(err, &typed) || typed.Code != "loop_cache_snapshot_too_large" || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("legacy version1 refunded public segment cap: %#v,%v", got, err)
		}
	}
	header, body := legacyChunks()
	header["version"] = "1"
	header["segmentTexts"] = []any{map[string]any{"text": strings.Repeat("x", 262145)}}
	got, err := ReadSnapshot(context.Background(), channelFixture(t, header, body, nil, nil, 10))
	var typed *ReadError
	if !errors.As(err, &typed) || typed.Code != "loop_cache_snapshot_too_large" || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("legacy version1 refunded public text field cap: %#v,%v", got, err)
	}
}

func TestReadSnapshotBothRecognizedSegmentArraysConsumeWork(t *testing.T) {
	header := map[string]any{
		"version": "1", "startIndex": 0, "segmentCount": 2, "length": 0,
		"segmentTexts": []any{nil, nil}, "segments": []any{nil, nil}, "headerMetadata": nil,
	}
	cap := testLimits()
	cap.segments = 3
	got, err := readSnapshot(context.Background(), channelFixture(t, header, nil, nil, nil, 10), cap)
	var typed *ReadError
	if !errors.As(err, &typed) || typed.Code != "loop_cache_snapshot_too_large" || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("recognized arrays replaced rather than charged independently: %#v,%v", got, err)
	}
}

func TestReadSnapshotRefusedChannelCatchupLimitsAreFatal(t *testing.T) {
	for _, content := range []string{
		strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65),
		"[" + strings.Repeat("null,", 131072) + "null]",
	} {
		header, body := legacyChunks()
		header["headerMetadata"] = nil
		root, value := objectFixture(t, channelFixture(t, header, body, []any{}, nil, 10))
		entries := value["blobContents"].(map[string]any)["$map"].([]any)
		entries[2].([]any)[1].(map[string]any)["$bytes"] = base64.StdEncoding.EncodeToString([]byte(content))
		got, err := ReadSnapshot(context.Background(), marshalFixture(t, root))
		var typed *ReadError
		if !errors.As(err, &typed) || typed.Code != "loop_cache_snapshot_too_large" || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("semantic channel refusal bypassed public catchup structural cap: %#v,%v", got, err)
		}
	}
}

func TestReadSnapshotLateRefusedCatchupDiscardsHealthyPeer(t *testing.T) {
	header, body := legacyChunks()
	root, value := objectFixture(t, channelFixture(t, header, body, nil, nil, 10))
	badHeader, _ := legacyChunks()
	badHeader["headerMetadata"] = nil
	contents := value["blobContents"].(map[string]any)
	contents["$map"] = append(contents["$map"].([]any),
		[]any{"bad-header", map[string]any{"$bytes": encodeBlob(t, badHeader)}},
		[]any{"bad-catchup", map[string]any{"$bytes": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)))}})
	children := value["snapshotTree"].(map[string]any)["trees"].(map[string]any)
	children["later"] = map[string]any{
		"blobs": map[string]any{"header": "bad-header", "catchupOps": "bad-catchup"}, "trees": map[string]any{},
	}
	got, err := ReadSnapshot(context.Background(), marshalFixture(t, root))
	var typed *ReadError
	if !errors.As(err, &typed) || typed.Code != "loop_cache_snapshot_too_large" || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("late refused channel published earlier healthy peer: %#v,%v", got, err)
	}
}

func TestReadSnapshotIgnoredTreeDoesNotDuplicateLongPrefixes(t *testing.T) {
	root, value := objectFixture(t, []byte(emptyEnvelope))
	children := map[string]any{}
	for i := 0; i < 1024; i++ {
		children[strconv.Itoa(i)] = map[string]any{"blobs": map[string]any{}, "trees": map[string]any{}}
	}
	value["snapshotTree"].(map[string]any)["trees"] = map[string]any{
		strings.Repeat("x", 16384): map[string]any{"blobs": map[string]any{}, "trees": children},
	}
	raw := marshalFixture(t, root)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := ReadSnapshot(context.Background(), raw)
	runtime.ReadMemStats(&after)
	if err != nil || len(got.Channels) != 0 || got.Partial {
		t.Fatalf("ignored valid tree changed reader outcome: %#v,%v", got, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("ignored 48KiB tree duplicated prefixes: allocated %d bytes, bound %d", allocated, 8<<20)
	}
}
