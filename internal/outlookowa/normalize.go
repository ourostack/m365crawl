package outlookowa

import (
	"errors"
	"sort"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

type identity struct{ kind, id string }

// Normalize only binds identities; the caller owns allocation atomicity and commit.
func (r Result) Normalize(resolve func(kind, sourceID string) (uint32, error)) (outlookmail.Result, error) {
	empty := outlookmail.Result{}
	if resolve == nil {
		return empty, errors.New("outlookowa: resolver_missing")
	}
	keys := map[identity]uint32{}
	add := func(kind, id string) bool {
		if !validID(id) {
			return false
		}
		keys[identity{kind, id}] = 0
		return true
	}
	if r.RootID != "" && !add("folder", r.RootID) {
		return empty, errors.New("outlookowa: invalid_native_id")
	}
	for _, f := range r.Folders {
		if !add("folder", f.ID) || !add("folder", f.ParentID) {
			return empty, errors.New("outlookowa: invalid_native_id")
		}
	}
	for _, m := range r.Messages {
		if !add("message", m.ID) || !add("folder", m.FolderID) || len(m.AttachmentIDs) != len(m.Value.Attachments) {
			return empty, errors.New("outlookowa: invalid_native_id")
		}
		for _, id := range m.AttachmentIDs {
			if !add("attachment", id) {
				return empty, errors.New("outlookowa: invalid_native_id")
			}
		}
	}
	for _, id := range r.SeenIDs {
		if !add("message", id) {
			return empty, errors.New("outlookowa: invalid_native_id")
		}
	}
	order := make([]identity, 0, len(keys))
	for id := range keys {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].kind != order[j].kind {
			return order[i].kind < order[j].kind
		}
		return order[i].id < order[j].id
	})
	for _, id := range order {
		key, err := resolve(id.kind, id.id)
		if err != nil {
			return empty, err
		}
		if key == 0 {
			return empty, errors.New("outlookowa: invalid_resolved_key")
		}
		keys[id] = key
	}
	key := func(kind, id string) uint32 { return keys[identity{kind, id}] }
	out := outlookmail.Result{RootKey: key("folder", r.RootID), Notes: r.Notes, Losses: append([]outlookmail.Loss(nil), r.Losses...), Doubtful: r.Doubtful}
	folders := map[string]outlookmail.Folder{}
	for _, f := range r.Folders {
		v := f.Value
		v.Key = key("folder", f.ID)
		v.Parent = key("folder", f.ParentID)
		folders[f.ID] = v
		out.Folders = append(out.Folders, v)
	}
	seen := map[uint32]bool{}
	for _, id := range r.SeenIDs {
		seen[key("message", id)] = true
	}
	coverage := map[uint32]outlookmail.Coverage{}
	for _, m := range r.Messages {
		v := m.Value
		k := key("message", m.ID)
		v.Header.Key = k
		v.Detail.Key = k
		v.DetailKey = k
		v.FolderKey = key("folder", m.FolderID)
		v.Folder = folders[m.FolderID]
		v.Folder.Key = v.FolderKey
		v.Attachments = append([]outlookmail.Attachment(nil), v.Attachments...)
		for i, id := range m.AttachmentIDs {
			v.Attachments[i].Key = key("attachment", id)
			v.Attachments[i].MessageKey = k
		}
		out.Messages = append(out.Messages, v)
		seen[k] = true
		c := coverage[v.FolderKey]
		c.FolderKey = v.FolderKey
		c.Count++
		if !v.Received.IsZero() {
			if c.Oldest.IsZero() || v.Received.Before(c.Oldest) {
				c.Oldest = v.Received
			}
			if c.Newest.IsZero() || v.Received.After(c.Newest) {
				c.Newest = v.Received
			}
		}
		coverage[v.FolderKey] = c
	}
	for k := range seen {
		out.SeenDetailKeys = append(out.SeenDetailKeys, k)
	}
	for _, c := range coverage {
		out.Coverage = append(out.Coverage, c)
	}
	sort.Slice(out.Folders, func(i, j int) bool { return out.Folders[i].Key < out.Folders[j].Key })
	sort.Slice(out.Messages, func(i, j int) bool { return out.Messages[i].Detail.Key < out.Messages[j].Detail.Key })
	sort.Slice(out.SeenDetailKeys, func(i, j int) bool { return out.SeenDetailKeys[i] < out.SeenDetailKeys[j] })
	sort.Slice(out.Coverage, func(i, j int) bool { return out.Coverage[i].FolderKey < out.Coverage[j].FolderKey })
	return out, nil
}
