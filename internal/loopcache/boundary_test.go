package loopcache

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func testLimits() limits {
	return limits{8 << 20, 1 << 20, 256 << 10, 4 << 20, 64, 131072, 4096, 4096, 1024, 65536, 64}
}

func objectFixture(t *testing.T, raw []byte) (map[string]any, map[string]any) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	return root, root["cachedObject"].(map[string]any)["value"].(map[string]any)
}

func marshalFixture(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReadSnapshotInputAndWorkBounds(t *testing.T) {
	header, body := legacyChunks()
	raw := channelFixture(t, header, body, nil, nil, 10)
	base := testLimits()
	base.input, base.blobs, base.nodes, base.channels, base.segments, base.path, base.text = len(raw), 2, 3, 1, 3, 2, 5
	base.strings = len("opaque") + len("channels/native") + len("A😀B")
	if _, err := readSnapshot(context.Background(), raw, base); err != nil {
		t.Fatalf("inclusive native bounds refused: %v", err)
	}
	for _, name := range []string{"input", "blob", "text", "strings", "depth", "members", "blobs", "nodes", "channels", "segments", "path"} {
		t.Run(name, func(t *testing.T) {
			cap := base
			switch name {
			case "input":
				cap.input--
			case "blob":
				cap.blob = 1
			case "text":
				cap.text--
			case "strings":
				cap.strings--
			case "depth":
				cap.depth = 1
			case "members":
				cap.members = 1
			case "blobs":
				cap.blobs--
			case "nodes":
				cap.nodes--
			case "channels":
				cap.channels--
			case "segments":
				cap.segments--
			case "path":
				cap.path--
			}
			got, err := readSnapshot(context.Background(), raw, cap)
			var typed *ReadError
			if !errors.As(err, &typed) || typed.Code != "loop_cache_snapshot_too_large" || !reflect.DeepEqual(got, Result{}) {
				t.Fatalf("%s bound published partial source: %#v,%v", name, got, err)
			}
		})
	}
}

func TestReadSnapshotGlobalLimitsPrecedeChannelRefusal(t *testing.T) {
	header, body := legacyChunks()
	header["segmentTexts"] = []any{map[string]any{"unsupported": true}, map[string]any{"marker": map[string]any{}}}
	body["segmentTexts"] = []any{map[string]any{"text": "B"}, map[string]any{"text": "C"}}
	cap := testLimits()
	cap.segments = 3
	got, err := readSnapshot(context.Background(), channelFixture(t, header, body, nil, nil, 10), cap)
	var typed *ReadError
	if !errors.As(err, &typed) || typed.Code != "loop_cache_snapshot_too_large" || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("semantic early refusal bypassed later chunk work bound: %#v,%v", got, err)
	}
}

func TestReadSnapshotRepeatedOrderedHeaderConsumesAttemptedWork(t *testing.T) {
	header, body := legacyChunks()
	header["headerMetadata"].(map[string]any)["orderedChunkMetadata"] = []any{map[string]any{"id": "header"}, map[string]any{"id": "header"}}
	cap := testLimits()
	cap.segments = 3
	got, err := readSnapshot(context.Background(), channelFixture(t, header, body, nil, nil, 10), cap)
	var typed *ReadError
	if !errors.As(err, &typed) || typed.Code != "loop_cache_snapshot_too_large" || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("refused repeated chunk occurrence refunded attempted work: %#v,%v", got, err)
	}
}

func TestReadSnapshotInnerStrictJSONRefusesChannelOnly(t *testing.T) {
	for _, raw := range []string{
		`{"segmentTexts":[],"segment\u0054exts":[]}`,
		`{"segmentTexts":[{"text":"\ud800"}]}`,
		`{"segmentTexts":[{"text":"` + string([]byte{0xff}) + `"}]}`,
		`{} {}`,
	} {
		header, body := legacyChunks()
		root, value := objectFixture(t, channelFixture(t, header, body, nil, nil, 10))
		entries := value["blobContents"].(map[string]any)["$map"].([]any)
		entries[0].([]any)[1].(map[string]any)["$bytes"] = base64.StdEncoding.EncodeToString([]byte(raw))
		got, err := ReadSnapshot(context.Background(), marshalFixture(t, root))
		if err != nil || len(got.Channels) != 0 || !got.Partial ||
			!reflect.DeepEqual(got.Losses, []Loss{{Code: "loop_cache_channel_unmapped", Count: 1}}) {
			t.Fatalf("inner malformed JSON confused with fatal outer input: %#v,%v", got, err)
		}
	}
}

func TestReadSnapshotMalformedSegmentsRefuseChannel(t *testing.T) {
	for _, segment := range []any{
		nil, "text", map[string]any{"text": 42}, map[string]any{"marker": "not-marker"},
		map[string]any{"text": "A", "marker": map[string]any{}}, map[string]any{"json": map[string]any{"text": "A"}},
		map[string]any{"unrecognized": "A"},
	} {
		header, body := legacyChunks()
		header["segmentTexts"] = []any{segment, map[string]any{"marker": map[string]any{}}}
		got, err := ReadSnapshot(context.Background(), channelFixture(t, header, body, nil, nil, 10))
		if err != nil || !got.Partial || len(got.Channels) != 0 ||
			!reflect.DeepEqual(got.Losses, []Loss{{Code: "loop_cache_channel_unmapped", Count: 1}}) {
			t.Fatalf("unsupported segment invented/skipped text: %#v,%v", got, err)
		}
	}
}

func TestReadSnapshotStrictOuterSyntax(t *testing.T) {
	for _, raw := range []string{
		`{"fileId":`, `{"fileId":"bad\`, `{"fileId":"\u12"}`, `{"fileId":"\ud800\u0041"}`, `{"fileId":"\uZZZZ"}`,
		`{"fileId":1,"cachedObject":{}}`, `{"fileId":"opaque","cachedObject":{"value":[]}}`,
		strings.Replace(emptyEnvelope, `"sequenceNumber":10`, `"sequenceNumber":-1`, 1),
		strings.Replace(emptyEnvelope, `"$map":[]`, `"$map":[["id"]]`, 1),
		strings.Replace(emptyEnvelope, `"$map":[]`, `"$map":[["",{"$bytes":""}]]`, 1),
		strings.Replace(emptyEnvelope, `"snapshotTree":{"blobs":{},"trees":{}}`, `"snapshotTree":null`, 1),
		strings.Replace(emptyEnvelope, `"blobs":{}`, `"blobs":{"header":null}`, 1),
		strings.Replace(emptyEnvelope, `"trees":{}`, `"trees":null`, 1),
	} {
		got, err := ReadSnapshot(context.Background(), []byte(raw))
		var typed *ReadError
		if !errors.As(err, &typed) || typed.Code != "loop_cache_snapshot_unsupported" || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("outer syntax/shape was not a whole-source refusal: %#v,%v", got, err)
		}
	}

}

type checkedContext struct {
	context.Context
	calls, cancelAt int
}

func (c *checkedContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestReadSnapshotLateFailure(t *testing.T) {
	header, body := legacyChunks()
	raw := channelFixture(t, header, body, []any{1}, nil, 10)
	for at := 1; at < 1000; at++ {
		ctx := &checkedContext{Context: context.Background(), cancelAt: at}
		got, err := ReadSnapshot(ctx, raw)
		if err == nil {
			if len(got.Channels) != 1 {
				t.Fatal("unarmed read lost channel")
			}
			return
		}
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("cancel boundary %d published prior channel: %#v,%v", at, got, err)
		}
	}
	t.Fatal("no completed boundary within bounded cancellation probe")
}

func TestReadSnapshotSafeErrorsAndEscapedUnicode(t *testing.T) {
	_, err := ReadSnapshot(context.Background(), []byte(`{"private-secret":"\ud800"}`))
	if err == nil || err.Error() != "loop_cache_snapshot_unsupported" {
		t.Fatalf("unsafe refusal: %v", err)
	}
	got, err := ReadSnapshot(context.Background(), []byte(strings.Replace(emptyEnvelope, `"opaque"`, `"\ud83d\ude00\\literal"`, 1)))
	if err != nil || got.FileID != "😀\\literal" {
		t.Fatalf("valid paired surrogate/escaped slash corrupted: %#v,%v", got, err)
	}
	for _, raw := range []string{
		`}`, `[`, `{1:2}`, `{"x"}`, `{"x":1`, `{"x":[]`, `{"x":0]`, `[1}`, `{"x":"\u`, `{"x":"\ud800\uxxxx"}`,
		strings.Replace(emptyEnvelope, `"blobContents":{"$map":[]}`, `"blobContents":{}`, 1),
		strings.Replace(emptyEnvelope, `"blobs":{}`, `"blobs":null`, 1),
		strings.Replace(emptyEnvelope, `"trees":{}`, `"trees":{"bad":null}`, 1),
		strings.Replace(emptyEnvelope, `"$map":[]`, `"$map":[["id",{"$bytes":"YQ==\n"}]]`, 1),
	} {
		got, err := ReadSnapshot(context.Background(), []byte(raw))
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("invalid syntax/native shape accepted: %#v,%v", got, err)
		}
	}
	cap := testLimits()
	cap.strings = 5
	if _, err := readSnapshot(context.Background(), []byte(emptyEnvelope), cap); err == nil {
		t.Fatal("file identity bypassed retention cap")
	}
}

func TestReadSnapshotChunkMetadataAndCatchupRefusal(t *testing.T) {
	for _, change := range []string{"wrong-first", "missing-id", "missing-chunk", "bad-chunk-json", "unknown-version", "bad-size", "bad-total", "bad-catchup", "bad-catchup-json"} {
		header, body := legacyChunks()
		switch change {
		case "wrong-first":
			header["headerMetadata"].(map[string]any)["orderedChunkMetadata"] = []any{map[string]any{"id": "body"}}
		case "missing-id":
			header["headerMetadata"].(map[string]any)["orderedChunkMetadata"] = []any{map[string]any{}}
		case "missing-chunk":
			header["headerMetadata"].(map[string]any)["orderedChunkMetadata"] = []any{map[string]any{"id": "header"}, map[string]any{"id": "unknown"}}
		case "unknown-version":
			body["version"] = "unsupported"
		case "bad-size":
			body["segmentTexts"] = []any{map[string]any{"text": "Too-long"}}
		case "bad-total":
			delete(header, "totalSegmentCount")
		}
		root, value := objectFixture(t, channelFixture(t, header, body, []any{}, nil, 10))
		entries := value["blobContents"].(map[string]any)["$map"].([]any)
		if change == "bad-chunk-json" {
			entries[1].([]any)[1].(map[string]any)["$bytes"] = base64.StdEncoding.EncodeToString([]byte("{"))
		}
		if change == "bad-catchup" {
			entries[2].([]any)[1].(map[string]any)["$bytes"] = encodeBlob(t, map[string]any{})
		}
		if change == "bad-catchup-json" {
			entries[2].([]any)[1].(map[string]any)["$bytes"] = base64.StdEncoding.EncodeToString([]byte("{"))
		}
		got, err := ReadSnapshot(context.Background(), marshalFixture(t, root))
		if err != nil || !got.Partial || len(got.Channels) != 0 {
			t.Fatalf("invalid %s published healthy channel: %#v,%v", change, got, err)
		}
	}
}

func TestReadSnapshotPartialIsolationAndSharedBlobAccounting(t *testing.T) {
	header, body := legacyChunks()
	root, value := objectFixture(t, channelFixture(t, header, body, nil, nil, 10))
	tree := value["snapshotTree"].(map[string]any)
	tree["blobs"].(map[string]any)["metadata"] = "absent"
	nodes := tree["trees"].(map[string]any)["channels"].(map[string]any)["trees"].(map[string]any)
	nodes["second"] = map[string]any{"blobs": map[string]any{"header": "h", "body": "b"}, "trees": map[string]any{}}
	got, err := ReadSnapshot(context.Background(), marshalFixture(t, root))
	if err != nil || len(got.Channels) != 2 || got.Channels[0].Path != "channels/native" ||
		got.Channels[1].Path != "channels/second" || !got.Partial || got.MissingBlobs != 1 ||
		!reflect.DeepEqual(got.Losses, []Loss{{Code: "loop_cache_blob_missing", Count: 1}}) {
		t.Fatalf("missing unrelated metadata suppressed healthy channels: %#v,%v", got, err)
	}

	cap := testLimits()
	cap.segments = 5
	got, err = readSnapshot(context.Background(), marshalFixture(t, root), cap)
	var typed *ReadError
	if !errors.As(err, &typed) || typed.Code != "loop_cache_snapshot_too_large" || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("shared blob refunded per-channel segment attempts: %#v,%v", got, err)
	}
	nodes["second"].(map[string]any)["blobs"] = map[string]any{"header": "missing"}
	got, err = ReadSnapshot(context.Background(), marshalFixture(t, root))
	if err != nil || len(got.Channels) != 1 || got.MissingBlobs != 2 ||
		!reflect.DeepEqual(got.Losses, []Loss{{Code: "loop_cache_blob_missing", Count: 2}, {Code: "loop_cache_channel_unmapped", Count: 1}}) {
		t.Fatalf("missing channel suppressed peer or lost coverage: %#v,%v", got, err)
	}
}

func TestReadSnapshotKnownV1IncompleteShapeIsUnrelated(t *testing.T) {
	got, err := ReadSnapshot(context.Background(), channelFixture(t, map[string]any{"version": "1", "startIndex": 0}, nil, nil, nil, 10))
	if err != nil || got.Partial || len(got.Channels) != 0 {
		t.Fatalf("version-only unrelated header falsely identified: %#v,%v", got, err)
	}
}

func TestReadSnapshotIncompletePartitionRefusesChannel(t *testing.T) {
	header, body := legacyChunks()
	header["headerMetadata"].(map[string]any)["orderedChunkMetadata"] = []any{map[string]any{"id": "header"}}
	got, err := ReadSnapshot(context.Background(), channelFixture(t, header, body, nil, nil, 10))
	if err != nil || !got.Partial || len(got.Channels) != 0 {
		t.Fatalf("incomplete partition claimed complete channel: %#v,%v", got, err)
	}
}

func TestReadSnapshotChannelPathRetentionBound(t *testing.T) {
	header, body := legacyChunks()
	cap := testLimits()
	cap.strings = len("opaque")
	got, err := readSnapshot(context.Background(), channelFixture(t, header, body, nil, nil, 10), cap)
	var typed *ReadError
	if !errors.As(err, &typed) || typed.Code != "loop_cache_snapshot_too_large" || !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("channel native path escaped retained-string cap: %#v,%v", got, err)
	}
}
