package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

// TranscriptHit is one part of a recorded call that a transcript search matched: the first
// matching entry of the part, and how many of its entries matched. At is that entry's absolute
// time (the part's start plus the entry's offset; the part's start when the entry has no offset).
type TranscriptHit struct {
	AccountID, CallID, ThreadID, PartKey string
	EventKey, Title                      string
	Ordinal                              int
	Speaker, Text                        string
	At                                   time.Time
	FetchedAt                            *time.Time
	Matches                              int
}

// TranscriptSearch matches the text of fetched transcript entries with FTS5, the same query
// language as Search, and collapses the matches to one hit per part of a call, newest first by the
// hit's time. With a blank query it lists the parts whose entries the filters select. A blank query
// with no filter is a usage error, as for Search. f.Speaker narrows to entries whose speaker
// contains the text, ignoring case; f.Since and f.Until bound each entry's time; f.Calls and
// f.State are not used. truncated says more parts matched than f.Limit (zero means DefaultLimit).
func (s *Store) TranscriptSearch(ctx context.Context, query string, f TranscriptFilter) (hits []TranscriptHit, truncated bool, err error) {
	var w where
	from := ` from transcript_entries e`
	if strings.TrimSpace(query) != "" {
		match := buildFTSQuery(query)
		if match == "" {
			return nil, false, searchUsage("search query has no searchable terms")
		}
		// The speaker column is matched by --from, so the words look in the text only.
		w.add(`transcript_fts match ?`, "text : ("+match+")")
		from = ` from transcript_fts join transcript_entries e on e.rowid=transcript_fts.rowid`
	} else if f.Speaker == "" && f.Since.IsZero() && f.Until.IsZero() {
		u := errs.Usage("a transcript search needs words to find or at least one filter (--from, --since, --until)")
		u.Fix = "Give words to find, or --from (a part of the speaker's name), --since or --until; `m365crawl transcripts` lists the recorded meetings."
		return nil, false, u
	}
	if f.Account != nil {
		w.add(`p.account_id=?`, accountString(f.Account))
	}
	if f.Speaker != "" {
		w.add(`instr(lower(e.speaker), lower(?))>0`, f.Speaker)
	}
	// An entry's time is its part's start plus its offset, in milliseconds since the epoch.
	const entryMS = `(cast(round((julianday(coalesce(p.starts_at, p.sent_at))-2440587.5)*86400000) as integer)+coalesce(e.start_ms, 0))`
	if !f.Since.IsZero() {
		w.add(entryMS+`>=?`, f.Since.UnixMilli())
	}
	if !f.Until.IsZero() {
		w.add(entryMS+`<?`, f.Until.UnixMilli())
	}
	//nolint:gosec // G202: the fragments are constants; values are placeholders
	q := `select e.rowid, p.account_id, p.call_id, p.part_key, p.thread_id, p.ordinal, coalesce(p.starts_at, p.sent_at), f.fetched_at, e.start_ms` + from + `
	  join transcript_parts p on p.account_id=e.account_id and p.part_key=e.part_key
	  left join transcript_fetches f on f.account_id=e.account_id and f.part_key=e.part_key` + w.sql() + `
	  order by p.account_id, p.call_id, p.ordinal, e.ord`
	rows, err := s.query(ctx, q, w.args...)
	if err != nil {
		return nil, false, err
	}
	var first []int64 // the rowid of each hit's first matching entry
	if hits, first, err = collapseHits(rows); err != nil {
		return nil, false, err
	}
	order := make([]int, len(hits))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return hits[order[a]].At.After(hits[order[b]].At) })
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if len(order) > limit {
		order, truncated = order[:limit], true
	}
	out := make([]TranscriptHit, len(order))
	ids := make([]any, len(order))
	for i, j := range order {
		out[i], ids[i] = hits[j], first[j]
	}
	if err := s.hitText(ctx, out, ids); err != nil {
		return nil, false, err
	}
	if err := s.nameHits(ctx, out); err != nil {
		return nil, false, err
	}
	return out, truncated, nil
}

// collapseHits groups matching entries, which come ordered by call, part and entry, into one hit
// per part.
func collapseHits(rows *sql.Rows) (hits []TranscriptHit, first []int64, err error) {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var h TranscriptHit
		var rowid int64
		var starts, fetched sql.NullString
		var offset sql.NullInt64
		if err := rows.Scan(&rowid, &h.AccountID, &h.CallID, &h.PartKey, &h.ThreadID, &h.Ordinal, &starts, &fetched, &offset); err != nil {
			return nil, nil, err
		}
		h.At = parseTime(starts).Add(time.Duration(offset.Int64) * time.Millisecond)
		if n := len(hits); n > 0 && hits[n-1].AccountID == h.AccountID && hits[n-1].CallID == h.CallID && hits[n-1].PartKey == h.PartKey {
			hits[n-1].Matches++
			continue
		}
		h.FetchedAt, h.Matches = timePtr(fetched), 1
		hits, first = append(hits, h), append(first, rowid)
	}
	return hits, first, rows.Err()
}

// hitText fills each hit's speaker and text from its first matching entry; ids holds their rowids
// in the order of hits.
func (s *Store) hitText(ctx context.Context, hits []TranscriptHit, ids []any) error {
	if len(hits) == 0 {
		return nil
	}
	rows, err := s.query(ctx, `select rowid, speaker, text from transcript_entries where rowid in `+inList(len(ids)), ids...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	at := map[int64]int{}
	for i, id := range ids {
		at[id.(int64)] = i
	}
	for rows.Next() {
		var id int64
		var speaker, text string
		if err := rows.Scan(&id, &speaker, &text); err != nil {
			return err
		}
		hits[at[id]].Speaker, hits[at[id]].Text = speaker, text
	}
	return rows.Err()
}

// nameHits fills each hit's title and event key the way TranscriptCalls names its calls.
func (s *Store) nameHits(ctx context.Context, hits []TranscriptHit) error {
	calls := make([]TranscriptCall, len(hits))
	for i, h := range hits {
		calls[i] = TranscriptCall{AccountID: h.AccountID, CallID: h.CallID, ThreadID: h.ThreadID}
	}
	if err := s.nameCalls(ctx, calls); err != nil {
		return err
	}
	for i, c := range calls {
		hits[i].Title, hits[i].EventKey = c.Title, c.EventKey
	}
	return nil
}

// HasTranscriptText reports whether any part of a recorded call has been fetched: its text is in
// the archive (fetched_at is set), even when the transcript had no entries. TranscriptStatus counts
// the same parts as fetched.
func (s *Store) HasTranscriptText(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select exists(select 1 from transcript_fetches f join transcript_parts p on p.account_id=f.account_id and p.part_key=f.part_key
	  where f.fetched_at is not null)`).Scan(&n)
	return n == 1, err
}

// TranscriptStatus is what the archive holds of recorded meetings: the calls, their parts (not
// counting the placeholder of a call with no file reference), the parts a fetch can ask for, those
// whose text is in the archive, and when a fetch last ran (nil when none ever did).
type TranscriptStatus struct {
	Calls       int        `json:"calls"`
	Parts       int        `json:"parts"`
	Fetchable   int        `json:"fetchable"`
	Fetched     int        `json:"fetched"`
	LastFetchAt *time.Time `json:"last_fetch_at"`
}

// TranscriptStatus counts the recorded calls and parts of every account.
func (s *Store) TranscriptStatus(ctx context.Context) (TranscriptStatus, error) {
	var out TranscriptStatus
	rows, err := s.query(ctx, `select `+partCols+` from transcript_parts p
	  left join transcript_fetches f on f.account_id=p.account_id and f.part_key=p.part_key order by p.account_id, p.call_id, p.ordinal`)
	if err != nil {
		return out, err
	}
	calls, err := scanParts(rows)
	if err != nil {
		return out, err
	}
	out.Calls = len(calls)
	for _, c := range calls {
		for _, p := range c.Parts {
			if p.RefQuality != transcripts.RefUnresolved {
				out.Parts++
			}
		}
		fetchable, fetched := c.Fetchable()
		out.Fetchable += fetchable
		out.Fetched += fetched
	}
	var last sql.NullString
	if err := s.db.QueryRowContext(ctx, `select max(attempted_at) from transcript_fetches`).Scan(&last); err != nil {
		return out, err
	}
	out.LastFetchAt = timePtr(last)
	return out, nil
}
