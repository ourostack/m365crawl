package loopcache

import "strings"

func isCandidate(header map[string]any) bool {
	if _, exists := header["segmentTexts"]; exists {
		return true
	}
	return isV1(header)
}

func isV1(header map[string]any) bool {
	if header["version"] != "1" {
		return false
	}
	for _, key := range []string{"startIndex", "segmentCount", "length", "segments", "headerMetadata"} {
		if _, exists := header[key]; !exists {
			return false
		}
	}
	return true
}

func orderedIDs(header map[string]any) ([]string, bool) {
	metadata, _ := header["headerMetadata"].(map[string]any)
	entries, ok := metadata["orderedChunkMetadata"].([]any)
	if !ok || len(entries) == 0 {
		return nil, false
	}
	ids := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		object, _ := entry.(map[string]any)
		id, _ := object["id"].(string)
		if !validName(id) || seen[id] {
			return nil, false
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, ids[0] == "header"
}

func project(node *treeNode, header map[string]any, load func(string) (any, error)) (Channel, error) {
	ids, valid := orderedIDs(header)
	metadata, _ := header["headerMetadata"].(map[string]any)
	sequence, good := integer(metadata["sequenceNumber"])
	totalLength, lengthOK := integer(metadata["totalLength"])
	totalCount, countOK := integer(metadata["totalSegmentCount"])
	if !valid || !good || !lengthOK || !countOK {
		return Channel{}, unsupported()
	}
	channel := Channel{Sequence: sequence}
	texts := []string{}
	var segments, length int64
	for _, id := range ids {
		blobID, exists := node.blobs[id]
		if !exists {
			return Channel{}, unsupported()
		}
		value, err := load(blobID)
		if err != nil {
			return Channel{}, err
		}
		chunk, _ := value.(map[string]any)
		if chunk["version"] != nil {
			return Channel{}, unsupported()
		}
		start, a := integer(chunk["chunkStartSegmentIndex"])
		count, b := integer(chunk["chunkSegmentCount"])
		size, c := integer(chunk["chunkLengthChars"])
		chunkTotal, d := integer(chunk["totalSegmentCount"])
		chunkLength, e := integer(chunk["totalLengthChars"])
		chunkSeq, f := integer(chunk["chunkSequenceNumber"])
		array, g := chunk["segmentTexts"].([]any)
		if !a || !b || !c || !d || !e || !f || !g || start != segments ||
			count != int64(len(array)) || chunkTotal != totalCount || chunkLength != totalLength || chunkSeq != sequence ||
			count > totalCount-segments || size > totalLength-length {
			return Channel{}, unsupported()
		}
		var actual int64
		for _, value := range array {
			segment, ok := value.(map[string]any)
			if !ok {
				return Channel{}, unsupported()
			}
			if _, wrapped := segment["json"]; wrapped {
				return Channel{}, unsupported()
			}
			text, hasText := segment["text"]
			marker, hasMarker := segment["marker"]
			if hasText == hasMarker {
				return Channel{}, unsupported()
			}
			if hasText {
				str, ok := text.(string)
				if !ok {
					return Channel{}, unsupported()
				}
				actual += utf16Length(str)
				texts = append(texts, str)
			} else {
				if _, ok := marker.(map[string]any); !ok {
					return Channel{}, unsupported()
				}
				channel.Markers++
				actual++
			}
		}
		if actual != size {
			return Channel{}, unsupported()
		}
		segments += count
		length += size
	}
	if segments != totalCount || length != totalLength {
		return Channel{}, unsupported()
	}
	if id, exists := node.blobs["catchupOps"]; exists {
		value, err := load(id)
		if err != nil {
			return Channel{}, err
		}
		ops, ok := value.([]any)
		if !ok {
			return Channel{}, unsupported()
		}
		channel.PendingOperations = len(ops)
	}
	channel.Text = strings.Join(texts, "")
	return channel, nil
}

func utf16Length(text string) int64 {
	var length int64
	for _, char := range text {
		length++
		if char > 0xffff {
			length++
		}
	}
	return length
}
