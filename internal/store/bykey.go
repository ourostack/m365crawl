package store

import (
	"context"
	"strings"
)

// byKeyChunk is the most keys one query looks up (4 bound values each, well under SQLite's limit).
const byKeyChunk = 200

// MessagesByKey returns the messages named by the keys Change.Key carries
// ("<tenant>|<user>|<conversation>|<id>"), deleted ones included, in the order of keys. Keys that
// are malformed or match no row are skipped.
func (s *Store) MessagesByKey(ctx context.Context, keys []string) ([]MessageRow, error) {
	byKey := map[string]MessageRow{}
	for lo := 0; lo < len(keys); lo += byKeyChunk {
		var w where
		var conds []string
		var args []any
		for _, k := range keys[lo:min(lo+byKeyChunk, len(keys))] {
			p := strings.SplitN(k, "|", 4)
			if len(p) != 4 {
				continue
			}
			conds = append(conds, `(m.tenant_id=? and m.user_id=? and m.conversation_id=? and m.id=?)`)
			args = append(args, p[0], p[1], p[2], p[3])
		}
		if len(conds) == 0 {
			continue
		}
		w.add(`(`+strings.Join(conds, ` or `)+`)`, args...)
		rows, _, err := s.runMessages(ctx, ` from messages m`+msgJoin, &w, byKeyChunk, nil)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			byKey[r.TenantID+"|"+r.UserID+"|"+r.ConversationID+"|"+r.ID] = r
		}
	}
	out := []MessageRow{}
	for _, k := range keys {
		if r, ok := byKey[k]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// ActivityByKey returns the activity items named by the keys Change.Key carries
// ("<tenant>|<user>|<id>") with their message and conversation, in the order of keys. Keys that
// are malformed or match no row are skipped.
func (s *Store) ActivityByKey(ctx context.Context, keys []string) ([]ActivityRow, error) {
	byKey := map[string]ActivityRow{}
	for lo := 0; lo < len(keys); lo += byKeyChunk {
		var w where
		var conds []string
		var args []any
		for _, k := range keys[lo:min(lo+byKeyChunk, len(keys))] {
			p := strings.SplitN(k, "|", 3)
			if len(p) != 3 {
				continue
			}
			conds = append(conds, `(a.tenant_id=? and a.user_id=? and a.id=?)`)
			args = append(args, p[0], p[1], p[2])
		}
		if len(conds) == 0 {
			continue
		}
		w.add(`(`+strings.Join(conds, ` or `)+`)`, args...)
		rows, _, err := s.activityRows(ctx, &w, byKeyChunk, true, nil)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			byKey[r.TenantID+"|"+r.UserID+"|"+r.ID] = r
		}
	}
	out := []ActivityRow{}
	for _, k := range keys {
		if r, ok := byKey[k]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}
