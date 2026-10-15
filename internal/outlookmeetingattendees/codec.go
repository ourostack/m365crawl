package outlookmeetingattendees

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"slices"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookcal"
)

type nativeRow struct {
	Key       uint32  `json:"native_key"`
	DetailKey uint32  `json:"detail_key"`
	Version   uint64  `json:"version_raw,string"`
	Block     int64   `json:"block_offset,string"`
	Position  int     `json:"payload_pos,string"`
	Resynced  bool    `json:"resynced"`
	Name      *string `json:"name"`
	Email     *string `json:"email"`
}

type selectionRow struct {
	ID        string `json:"native_event_id"`
	Modified  uint64 `json:"last_modified_raw,string"`
	Stamp     uint64 `json:"stamp_raw,string"`
	Block     int64  `json:"block_offset,string"`
	Position  int    `json:"payload_pos,string"`
	Key       uint32 `json:"detail_key"`
	Copies    int    `json:"detail_copies"`
	Resynced  int    `json:"detail_resynced_copies"`
	Present   bool   `json:"detail_present"`
	Differing bool   `json:"detail_differing"`
}

type parentRow struct {
	Key    uint32 `json:"detail_key"`
	Copies int    `json:"detail_copies"`
	Links  int    `json:"event_links"`
}

type lossRow struct {
	Code  string `json:"code"`
	Count int    `json:"count,string"`
}

func toNative(a outlookattendees.Attendee) nativeRow {
	return nativeRow{a.Key, a.DetailKey, a.VersionRaw, a.BlockOffset, a.PayloadPos, a.Resynced, a.Name, a.Email}
}

func (a nativeRow) observation() outlookattendees.Attendee {
	return outlookattendees.Attendee{Key: a.Key, DetailKey: a.DetailKey, VersionRaw: a.Version,
		BlockOffset: a.Block, PayloadPos: a.Position, Resynced: a.Resynced, Name: a.Name, Email: a.Email}
}

func toSelection(s outlookcal.SelectionReceipt) selectionRow {
	return selectionRow{s.SourceID, s.LastModifiedRaw, s.StampRaw, s.BlockOffset, s.PayloadPos,
		s.DetailKey, s.DetailCopies, s.DetailResyncedCopies, s.DetailPresent, s.DetailDiffering}
}

func (s selectionRow) receipt() outlookcal.SelectionReceipt {
	return outlookcal.SelectionReceipt{SourceID: s.ID, LastModifiedRaw: s.Modified, StampRaw: s.Stamp,
		BlockOffset: s.Block, PayloadPos: s.Position, DetailKey: s.Key, DetailCopies: s.Copies,
		DetailResyncedCopies: s.Resynced, DetailPresent: s.Present, DetailDiffering: s.Differing}
}

func checkEnvelopeSize(size, limit int) error {
	if size < 0 || size > limit {
		return ErrTooLarge
	}
	return nil
}

func Encode(ctx context.Context, e Evidence) ([]byte, error) {
	var b bytes.Buffer
	if err := writeEvidence(ctx, &b, e); err != nil {
		return nil, err
	}
	raw := b.Bytes()
	if _, err := Audit(ctx, raw, nil); err != nil {
		return nil, err
	}
	return raw, nil
}

func writeEvidence(ctx context.Context, w io.Writer, e Evidence) error {
	return writeEvidenceBounded(ctx, w, e, MaxEnvelopeBytes)
}

func writeEvidenceBounded(ctx context.Context, w io.Writer, e Evidence, limit int) error {
	if len(e.Attendees) > 262144 || len(e.Selections) > 65536 || len(e.Parents) > 65536 || len(e.AmbiguousKeys) > 262144 || len(e.Losses) > 7 {
		return ErrTooLarge
	}
	stringBytes, idBytes := 0, 0
	for _, a := range e.Attendees {
		for _, text := range []*string{a.Name, a.Email} {
			if text == nil {
				continue
			}
			if !utf8.ValidString(*text) {
				return ErrInvalid
			}
			if len(*text) > 262144 || len(*text) > 16777216-stringBytes {
				return ErrTooLarge
			}
			stringBytes += len(*text)
		}
	}
	for _, s := range e.Selections {
		if !utf8.ValidString(s.SourceID) {
			return ErrInvalid
		}
		if len(s.SourceID) > 262144 || len(s.SourceID) > 8388608-idBytes {
			return ErrTooLarge
		}
		idBytes += len(s.SourceID)
	}
	written := 0
	write := func(raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(raw) > limit-written {
			return ErrTooLarge
		}
		n, err := w.Write(raw)
		written += n
		if err == nil && n != len(raw) {
			return io.ErrShortWrite
		}
		return err
	}
	if err := write([]byte(`{"format":"outlook-attendee-evidence-v1","selections":[`)); err != nil {
		return err
	}
	arrays := []struct {
		n     int
		value func(int) any
		end   string
	}{
		{len(e.Selections), func(i int) any { return toSelection(e.Selections[i]) }, `],"attendees":[`},
		{len(e.Attendees), func(i int) any { return toNative(e.Attendees[i]) }, `],"ambiguous_keys":[`},
		{len(e.AmbiguousKeys), func(i int) any { return e.AmbiguousKeys[i] }, `],"parents":[`},
		{len(e.Parents), func(i int) any { p := e.Parents[i]; return parentRow{p.DetailKey, p.DetailCopies, p.EventLinks} }, `],"losses":[`},
		{len(e.Losses), func(i int) any { l := e.Losses[i]; return lossRow{l.Code, l.Count} }, `]}`},
	}
	for _, array := range arrays {
		for i := range array.n {
			if i != 0 {
				if err := write([]byte(",")); err != nil {
					return err
				}
			}
			raw, _ := json.Marshal(array.value(i)) // fixed owned structs contain no fallible JSON types
			if err := write(raw); err != nil {
				return err
			}
		}
		if err := write([]byte(array.end)); err != nil {
			return err
		}
	}
	return nil
}

type cursor struct {
	raw []byte
	pos int
}

func (c *cursor) literal(s string) error {
	if !bytes.HasPrefix(c.raw[c.pos:], []byte(s)) {
		return ErrInvalid
	}
	c.pos += len(s)
	return nil
}

func (c *cursor) row(dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(c.raw[c.pos:]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return ErrInvalid
	}
	raw, _ := json.Marshal(dst) // decoded fixed owned structs contain only JSON scalar types
	n := int(decoder.InputOffset())
	if !bytes.Equal(raw, c.raw[c.pos:c.pos+n]) {
		return ErrInvalid
	}
	c.pos += n
	return nil
}

func (c *cursor) array(ctx context.Context, limit int, visit func(int) error) error {
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if c.pos < len(c.raw) && c.raw[c.pos] == ']' {
			c.pos++
			return nil
		}
		if i >= limit {
			return ErrTooLarge
		}
		if i != 0 {
			if err := c.literal(","); err != nil {
				return err
			}
		}
		if err := visit(i); err != nil {
			return err
		}
	}
}

func Audit(ctx context.Context, raw []byte, visitor Visitor) (Summary, error) {
	return auditBounded(ctx, raw, visitor, MaxEnvelopeBytes)
}

func auditBounded(ctx context.Context, raw []byte, visitor Visitor, limit int) (Summary, error) {
	return auditVisitorsBounded(ctx, raw, visitor, nil, limit)
}

func AuditVisitors(ctx context.Context, raw []byte, visitor Visitor, selections SelectionVisitor) (Summary, error) {
	return auditVisitorsBounded(ctx, raw, visitor, selections, MaxEnvelopeBytes)
}

func auditVisitorsBounded(ctx context.Context, raw []byte, visitor Visitor, selections SelectionVisitor, limit int) (Summary, error) {
	var staged Summary
	if err := checkEnvelopeSize(len(raw), limit); err != nil {
		return Summary{}, err
	}
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	// Pass one validates every relationship before publishing any selected rows.
	if err := scanEvidence(ctx, raw, &staged, nil, nil, nil); err != nil {
		return Summary{}, err
	}
	if visitor != nil || selections != nil {
		if err := scanEvidence(ctx, raw, nil, visitor, nil, selections); err != nil {
			return Summary{}, err
		}
	}
	return staged, nil
}

func Decode(ctx context.Context, raw []byte) (Evidence, error) {
	if _, err := Audit(ctx, raw, nil); err != nil {
		return Evidence{}, err
	}
	e := Evidence{Attendees: []outlookattendees.Attendee{}, AmbiguousKeys: []uint32{},
		Parents: []outlookattendees.Evidence{}, Losses: []outlookattendees.Loss{}, Selections: []outlookcal.SelectionReceipt{}}
	if err := scanEvidence(ctx, raw, nil, nil, &e, nil); err != nil {
		return Evidence{}, err
	}
	return e, nil
}

func scanEvidence(ctx context.Context, raw []byte, summary *Summary, visitor Visitor, e *Evidence, selectionVisitor SelectionVisitor) error {
	c := cursor{raw: raw}
	if err := c.literal(`{"format":"outlook-attendee-evidence-v1","selections":[`); err != nil {
		return err
	}
	keys := map[uint32]bool{}
	addKey := func(key uint32) error {
		if !keys[key] && len(keys) >= 65536 {
			return ErrTooLarge
		}
		keys[key] = true
		return nil
	}
	selectionDetails := map[uint32]int{}
	selectionCopies := map[[32]byte]bool{}
	idBytes := 0
	selections := 0
	if err := c.array(ctx, 65536, func(i int) error {
		var s selectionRow
		if err := c.row(&s); err != nil {
			return err
		}
		idBytes += len(s.ID)
		if len(s.ID) > 262144 || idBytes > 8388608 {
			return ErrTooLarge
		}
		if s.ID == "" || !utf8.ValidString(s.ID) || s.Position < 0 || s.Copies < 0 || s.Copies > 524288 ||
			s.Resynced < 0 || s.Resynced > s.Copies || (s.Differing && s.Copies-s.Resynced < 2) {
			return ErrInvalid
		}
		canonical, _ := json.Marshal(s)
		digest := sha256.Sum256(canonical)
		if selectionCopies[digest] {
			return ErrInvalid
		}
		selectionCopies[digest] = true
		if s.Key != 0 {
			if s.Present != (s.Copies > s.Resynced) {
				return ErrInvalid
			}
			keys[s.Key] = true // at most one key per already bounded selection row
			if copies, held := selectionDetails[s.Key]; held && copies != s.Copies {
				return ErrInvalid
			}
			selectionDetails[s.Key] = s.Copies
		}
		selections++
		if e != nil {
			e.Selections = append(e.Selections, s.receipt())
		}
		if selectionVisitor != nil {
			return selectionVisitor(i, s.receipt())
		}
		return nil
	}); err != nil {
		return err
	}
	if err := c.literal(`,"attendees":[`); err != nil {
		return err
	}
	first := map[uint32][32]byte{}
	conflicts := map[uint32]bool{}
	parents := map[uint32]bool{}
	stringsTotal, attendees := 0, 0
	if err := c.array(ctx, 262144, func(i int) error {
		var a nativeRow
		if err := c.row(&a); err != nil {
			return err
		}
		if a.Key == 0 || a.DetailKey == 0 || a.Position < 0 {
			return ErrInvalid
		}
		for _, text := range []*string{a.Name, a.Email} {
			if text == nil {
				continue
			}
			stringsTotal += len(*text)
			if len(*text) > 262144 || stringsTotal > 16777216 {
				return ErrTooLarge
			}
		}
		if err := addKey(a.DetailKey); err != nil {
			return err
		}
		parents[a.DetailKey] = true
		logical := a
		logical.Block, logical.Position = 0, 0
		signature, _ := json.Marshal(logical)
		digest := sha256.Sum256(signature)
		if old, held := first[a.Key]; held && old != digest {
			conflicts[a.Key] = true
		} else if !held {
			first[a.Key] = digest
		}
		attendees++
		if e != nil {
			e.Attendees = append(e.Attendees, a.observation())
		}
		if visitor != nil {
			return visitor(i, a.observation())
		}
		return nil
	}); err != nil {
		return err
	}
	if err := c.literal(`,"ambiguous_keys":[`); err != nil {
		return err
	}
	ambiguous := []uint32{}
	if err := c.array(ctx, 262144, func(_ int) error {
		var key uint32
		if err := c.row(&key); err != nil {
			return err
		}
		if !conflicts[key] || (len(ambiguous) > 0 && ambiguous[len(ambiguous)-1] >= key) {
			return ErrInvalid
		}
		ambiguous = append(ambiguous, key)
		return nil
	}); err != nil {
		return err
	}
	if len(ambiguous) != len(conflicts) {
		return ErrInvalid
	}
	if err := c.literal(`,"parents":[`); err != nil {
		return err
	}
	parentKeys := []uint32{}
	missingDetail, missingEvent := 0, 0
	if err := c.array(ctx, 65536, func(_ int) error {
		var p parentRow
		if err := c.row(&p); err != nil {
			return err
		}
		if !parents[p.Key] || (len(parentKeys) > 0 && parentKeys[len(parentKeys)-1] >= p.Key) ||
			p.Copies < 0 || p.Links < 0 || p.Copies > 524288 || p.Links > 524288 {
			return ErrInvalid
		}
		if copies, held := selectionDetails[p.Key]; held && copies != p.Copies {
			return ErrInvalid
		}
		parentKeys = append(parentKeys, p.Key)
		if p.Copies == 0 {
			missingDetail++
		}
		if p.Links == 0 {
			missingEvent++
		}
		if e != nil {
			e.Parents = append(e.Parents, outlookattendees.Evidence{DetailKey: p.Key, DetailCopies: p.Copies, EventLinks: p.Links})
		}
		return nil
	}); err != nil {
		return err
	}
	if len(parentKeys) != len(parents) {
		return ErrInvalid
	}
	if err := c.literal(`,"losses":[`); err != nil {
		return err
	}
	losses := []outlookattendees.Loss{}
	allowed := []string{"outlook_attendees_detail_missing", "outlook_attendees_event_link_missing", "outlook_attendees_geometry_resynced", "outlook_attendees_geometry_unmapped", "outlook_attendees_resynced", "outlook_attendees_unmapped", "outlook_blocks_damaged"}
	if err := c.array(ctx, 7, func(_ int) error {
		var l lossRow
		if err := c.row(&l); err != nil {
			return err
		}
		if !slices.Contains(allowed, l.Code) || l.Count <= 0 || (len(losses) > 0 && losses[len(losses)-1].Code >= l.Code) {
			return ErrInvalid
		}
		losses = append(losses, outlookattendees.Loss{Code: l.Code, Count: l.Count})
		return nil
	}); err != nil {
		return err
	}
	found := func(code string) int {
		for _, l := range losses {
			if l.Code == code {
				return l.Count
			}
		}
		return 0
	}
	if found("outlook_attendees_detail_missing") != missingDetail || found("outlook_attendees_event_link_missing") != missingEvent {
		return ErrInvalid
	}
	if err := c.literal("}"); err != nil {
		return err
	}
	if c.pos != len(raw) {
		return ErrInvalid
	}
	if summary != nil {
		*summary = Summary{AttendeesTotal: attendees, ParentsTotal: len(parentKeys), SelectionsTotal: selections, AmbiguousKeys: ambiguous, Losses: losses}
	}
	if e != nil {
		e.AmbiguousKeys, e.Losses = ambiguous, losses
	}
	return ctx.Err()
}
