package loopcache

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"strings"
)

type limits struct {
	input, blob, text, strings                             int
	depth, members, blobs, nodes, channels, segments, path int
}

func ReadSnapshot(ctx context.Context, raw []byte) (Result, error) {
	return readSnapshot(ctx, raw, limits{8 << 20, 1 << 20, 256 << 10, 4 << 20, 64, 131072, 4096, 4096, 1024, 65536, 64})
}

type treeNode struct {
	parent *treeNode
	name   string
	blobs  map[string]string
}

func readSnapshot(ctx context.Context, raw []byte, cap limits) (result Result, err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			result = Result{}
		}
	}()
	if len(raw) > cap.input {
		return Result{}, tooLarge()
	}
	budget := &jsonBudget{ctx: ctx, maxDepth: cap.depth, maxItems: cap.members}
	parsed, err := budget.parse(raw)
	if err != nil {
		return Result{}, err
	}
	root, _ := parsed.(map[string]any)
	id, _ := root["fileId"].(string)
	cached, _ := root["cachedObject"].(map[string]any)
	value, _ := cached["value"].(map[string]any)
	sequence, valid := integer(value["sequenceNumber"])
	latest, good := integer(value["latestSequenceNumber"])
	ops, present := value["ops"].([]any)
	if id == "" || !valid || !good || latest < sequence || !present {
		return Result{}, unsupported()
	}
	if len(id) > cap.strings {
		return Result{}, tooLarge()
	}
	result = Result{FileID: id, Sequence: sequence, LatestSequence: latest, PendingOperations: len(ops), Partial: len(ops) > 0 || latest != sequence}
	contents, _ := value["blobContents"].(map[string]any)
	entries, ok := contents["$map"].([]any)
	if !ok {
		return Result{}, unsupported()
	}
	if len(entries) > cap.blobs {
		return Result{}, tooLarge()
	}
	blobs := map[string][]byte{}
	for _, entry := range entries {
		pair, ok := entry.([]any)
		if !ok || len(pair) != 2 {
			return Result{}, unsupported()
		}
		id, _ := pair[0].(string)
		payload, _ := pair[1].(map[string]any)
		encoded, ok := payload["$bytes"].(string)
		if id == "" || !ok {
			return Result{}, unsupported()
		}
		if _, exists := blobs[id]; exists {
			return Result{}, unsupported()
		}
		if len(encoded) > cap.blob || base64.StdEncoding.DecodedLen(len(encoded)) > cap.blob {
			return Result{}, tooLarge()
		}
		bytes, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || base64.StdEncoding.EncodeToString(bytes) != encoded {
			return Result{}, unsupported()
		}
		blobs[id] = bytes
	}
	nodes, missing := []*treeNode{}, map[string]bool{}
	var walk func(any, *treeNode, string, int) error
	walk = func(value any, parent *treeNode, name string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > cap.path || len(nodes) >= cap.nodes {
			return tooLarge()
		}
		node, ok := value.(map[string]any)
		if !ok {
			return unsupported()
		}
		refs, ok := node["blobs"].(map[string]any)
		if !ok {
			return unsupported()
		}
		children, ok := node["trees"].(map[string]any)
		if !ok {
			return unsupported()
		}
		one := &treeNode{parent: parent, name: name, blobs: map[string]string{}}
		for name, value := range refs {
			id, ok := value.(string)
			if !validName(name) || !ok || id == "" {
				return unsupported()
			}
			one.blobs[name] = id
			if _, exists := blobs[id]; !exists {
				missing[id] = true
			}
		}
		nodes = append(nodes, one)
		names := make([]string, 0, len(children))
		for name := range children {
			if !validName(name) {
				return unsupported()
			}
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := walk(children[name], one, name, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(value["snapshotTree"], nil, "", 0); err != nil {
		return Result{}, err
	}
	if len(missing) > 0 {
		result.MissingBlobs = len(missing)
		result.Partial = true
		result.Losses = []Loss{{Code: "loop_cache_blob_missing", Count: len(missing)}}
	}
	type parsedBlob struct {
		value any
		err   error
	}
	cache := map[string]parsedBlob{}
	load := func(id string) (any, error) {
		if entry, exists := cache[id]; exists {
			return entry.value, entry.err
		}
		raw, exists := blobs[id]
		if !exists {
			return nil, unsupported()
		}
		value, err := budget.parse(raw)
		cache[id] = parsedBlob{value: value, err: err}
		return value, err
	}
	fatal := func(err error) bool {
		if err == nil {
			return false
		}
		var typed *ReadError
		return !errors.As(err, &typed) || typed.Code == "loop_cache_snapshot_too_large"
	}
	chargeChannels, chargeSegments, retained, unmapped := 0, 0, len(id), 0
	for _, node := range nodes {
		headerID, exists := node.blobs["header"]
		if !exists {
			continue
		}
		value, loadErr := load(headerID)
		if fatal(loadErr) {
			return Result{}, loadErr
		}
		header, _ := value.(map[string]any)
		if loadErr == nil && !isCandidate(header) {
			continue
		}
		chargeChannels++
		if chargeChannels > cap.channels {
			return Result{}, tooLarge()
		}
		if id, exists := node.blobs["catchupOps"]; exists {
			if _, err := load(id); fatal(err) {
				return Result{}, err
			}
		}
		chunkIDs := []string{"header"}
		metadata, _ := header["headerMetadata"].(map[string]any)
		entries, _ := metadata["orderedChunkMetadata"].([]any)
		headerOccurrences := 0
		for _, entry := range entries {
			object, _ := entry.(map[string]any)
			id, _ := object["id"].(string)
			if id == "header" {
				headerOccurrences++
				if headerOccurrences == 1 {
					continue
				}
			}
			if id != "" {
				chunkIDs = append(chunkIDs, id)
			}
		}
		for _, id := range chunkIDs {
			value, err := load(node.blobs[id])
			if fatal(err) {
				return Result{}, err
			}
			chunk, _ := value.(map[string]any)
			legacy, _ := chunk["segmentTexts"].([]any)
			arrays := [][]any{legacy}
			if isV1(chunk) {
				newer, _ := chunk["segments"].([]any)
				arrays = append(arrays, newer)
			}
			for _, segments := range arrays {
				if len(segments) > cap.segments-chargeSegments {
					return Result{}, tooLarge()
				}
				chargeSegments += len(segments)
				for _, value := range segments {
					segment, _ := value.(map[string]any)
					text, _ := segment["text"].(string)
					if len(text) > cap.text {
						return Result{}, tooLarge()
					}
				}
			}
		}
		channel, err := project(node, header, load)
		if loadErr != nil || err != nil {
			unmapped++
			result.Partial = true
			continue
		}
		path, err := channelPath(node, cap.strings-retained)
		if err != nil {
			return Result{}, err
		}
		channel.Path = path
		retained += len(channel.Path)
		if len(channel.Text) > cap.strings-retained {
			return Result{}, tooLarge()
		}
		retained += len(channel.Text)
		if channel.PendingOperations > 0 || channel.Sequence != result.Sequence {
			result.Partial = true
		}
		result.Channels = append(result.Channels, channel)
	}
	if unmapped > 0 {
		result.Losses = append(result.Losses, Loss{Code: "loop_cache_channel_unmapped", Count: unmapped})
	}
	sort.Slice(result.Channels, func(i, j int) bool { return result.Channels[i].Path < result.Channels[j].Path })
	return result, nil
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.Contains(name, "/")
}

func channelPath(node *treeNode, remaining int) (string, error) {
	names := []string{}
	length := 0
	for current := node; current.parent != nil; current = current.parent {
		if len(names) > 0 {
			length++
		}
		if len(current.name) > remaining-length {
			return "", tooLarge()
		}
		length += len(current.name)
		names = append(names, current.name)
	}
	for i := 0; i < len(names)/2; i++ {
		j := len(names) - i - 1
		names[i], names[j] = names[j], names[i]
	}
	return strings.Join(names, "/"), nil
}
