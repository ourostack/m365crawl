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

const emptyEnvelope = `{"fileId":"opaque","cachedObject":{"value":{"sequenceNumber":10,"latestSequenceNumber":10,"ops":[],"snapshotTree":{"blobs":{},"trees":{}},"blobContents":{"$map":[]}}}}`

func TestReadSnapshotEnvelope(t *testing.T) {
	got, err := ReadSnapshot(context.Background(), []byte(emptyEnvelope))
	want := Result{FileID: "opaque", Sequence: 10, LatestSequence: 10}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("native envelope mappings missing: %#v,%v", got, err)
	}
}

func TestReadSnapshotIdentityRefusal(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `[]`,
		strings.Replace(emptyEnvelope, `"fileId":"opaque"`, `"fileId":"opaque","file\u0049d":"other"`, 1),
		strings.Replace(emptyEnvelope, `"opaque"`, `"\ud800"`, 1),
		strings.Replace(emptyEnvelope, `"opaque"`, `"\udc00"`, 1),
		strings.Replace(emptyEnvelope, `"opaque"`, "\"\xff\"", 1),
		emptyEnvelope + `{}`,
		strings.Replace(emptyEnvelope, `"sequenceNumber":10`, `"sequenceNumber":1e1`, 1),
		strings.Replace(emptyEnvelope, `"sequenceNumber":10`, `"sequenceNumber":9223372036854775808`, 1),
		strings.Replace(emptyEnvelope, `"latestSequenceNumber":10`, `"latestSequenceNumber":9`, 1),
		strings.Replace(emptyEnvelope, `"$map":[]`, `"$map":[["id",{"$bytes":"?"}]]`, 1),
		strings.Replace(emptyEnvelope, `"$map":[]`, `"$map":[["id",{"$bytes":""}],["id",{"$bytes":""}]]`, 1),
		strings.Replace(emptyEnvelope, `"trees":{}`, `"trees":{"../bad":{"blobs":{},"trees":{}}}`, 1),
	} {
		got, err := ReadSnapshot(context.Background(), []byte(raw))
		var typed *ReadError
		if !errors.As(err, &typed) || typed.Code != "loop_cache_snapshot_unsupported" || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("unsupported envelope/identity accepted: %#v,%v", got, err)
		}
	}
}

func channelFixture(t *testing.T, header, body any, catchup []any, ops []any, latest int64) []byte {
	t.Helper()
	refs := map[string]any{"header": "h"}
	entries := []any{[]any{"h", map[string]any{"$bytes": encodeBlob(t, header)}}}
	if body != nil {
		refs["body"] = "b"
		entries = append(entries, []any{"b", map[string]any{"$bytes": encodeBlob(t, body)}})
	}
	if catchup != nil {
		refs["catchupOps"] = "o"
		entries = append(entries, []any{"o", map[string]any{"$bytes": encodeBlob(t, catchup)}})
	}
	if ops == nil {
		ops = []any{}
	}
	value := map[string]any{
		"sequenceNumber": 10, "latestSequenceNumber": latest, "ops": ops,
		"blobContents": map[string]any{"$map": entries},
		"snapshotTree": map[string]any{"blobs": map[string]any{}, "trees": map[string]any{
			"channels": map[string]any{"blobs": map[string]any{}, "trees": map[string]any{
				"native": map[string]any{"blobs": refs, "trees": map[string]any{}},
			}},
		}},
	}
	raw, err := json.Marshal(map[string]any{"fileId": "opaque", "cachedObject": map[string]any{"value": value}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func legacyChunks() (map[string]any, map[string]any) {
	header := map[string]any{
		"chunkStartSegmentIndex": 0, "chunkSegmentCount": 2, "chunkLengthChars": 4,
		"totalLengthChars": 5, "totalSegmentCount": 3, "chunkSequenceNumber": 10,
		"segmentTexts": []any{map[string]any{"text": "A😀"}, map[string]any{"marker": map[string]any{}}},
		"headerMetadata": map[string]any{
			"sequenceNumber": 10, "totalLength": 5, "totalSegmentCount": 3,
			"orderedChunkMetadata": []any{map[string]any{"id": "header"}, map[string]any{"id": "body"}},
		},
	}
	body := map[string]any{
		"chunkStartSegmentIndex": 2, "chunkSegmentCount": 1, "chunkLengthChars": 1,
		"totalLengthChars": 5, "totalSegmentCount": 3, "chunkSequenceNumber": 10,
		"segmentTexts": []any{map[string]any{"text": "B"}},
	}
	return header, body
}

func TestReadSnapshotNativeSegments(t *testing.T) {
	header, body := legacyChunks()
	got, err := ReadSnapshot(context.Background(), channelFixture(t, header, body, nil, nil, 10))
	want := Result{FileID: "opaque", Sequence: 10, LatestSequence: 10,
		Channels: []Channel{{Path: "channels/native", Sequence: 10, Text: "A😀B", Markers: 1}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("native channel text/UTF16 geometry lost: %#v,%v", got, err)
	}
	got, err = ReadSnapshot(context.Background(), channelFixture(t, header, body, []any{1, 2}, []any{3}, 11))
	want.LatestSequence, want.PendingOperations, want.Partial = 11, 1, true
	want.Channels[0].PendingOperations = 2
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("global and channel operations conflated/applied: %#v,%v", got, err)
	}
}

func TestReadSnapshotChunkPartition(t *testing.T) {
	for _, field := range []string{"chunkStartSegmentIndex", "chunkSegmentCount", "chunkLengthChars", "totalLengthChars", "totalSegmentCount", "chunkSequenceNumber"} {
		header, body := legacyChunks()
		body[field] = 99
		got, err := ReadSnapshot(context.Background(), channelFixture(t, header, body, nil, nil, 10))
		if err != nil || len(got.Channels) != 0 || !got.Partial ||
			!reflect.DeepEqual(got.Losses, []Loss{{Code: "loop_cache_channel_unmapped", Count: 1}}) {
			t.Fatalf("inconsistent chunk %s assembled as healthy text: %#v,%v", field, got, err)
		}
	}
}

func TestReadSnapshotKnownUnsupportedVersusUnrelatedDDS(t *testing.T) {
	for _, test := range []struct {
		header any
		loss   bool
	}{
		{map[string]any{"version": "1", "startIndex": 0, "segmentCount": 0, "length": 0, "segments": []any{}, "headerMetadata": nil}, true},
		{map[string]any{"version": "other", "segmentTexts": []any{}}, true},
		{map[string]any{"version": "unrelated", "data": map[string]any{}}, false},
	} {
		got, err := ReadSnapshot(context.Background(), channelFixture(t, test.header, nil, nil, nil, 10))
		if err != nil || got.Partial != test.loss || len(got.Channels) != 0 || (len(got.Losses) != 0) != test.loss {
			t.Fatalf("unsupported channel confused with unrelated DDS: %#v,%v", got, err)
		}
	}
}

func encodeBlob(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}
