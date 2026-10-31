package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

// WorkIQWindowScope is the frontier scope of one mail window. Bounds are part of the position, so
// changed bounds start new positions; mode and filter are part of each resource.
func WorkIQWindowScope(since, until time.Time) string {
	return "window:" + since.UTC().Format(time.RFC3339) + "/" + until.UTC().Format(time.RFC3339)
}

// WorkIQWindowDeltaResource is a folder's filtered delta walk from since.
func WorkIQWindowDeltaResource(folderID string, since time.Time) string {
	return "/me/mailFolders/" + url.PathEscape(folderID) + "/messages/delta?$filter=receivedDateTime%20ge%20" + since.UTC().Format(time.RFC3339)
}

// WorkIQWindowListingResource is a folder's listing of [since, until), in a stable order.
func WorkIQWindowListingResource(folderID string, since, until time.Time) string {
	return "/me/mailFolders/" + url.PathEscape(folderID) + "/messages?$filter=receivedDateTime%20ge%20" + since.UTC().Format(time.RFC3339) +
		"%20and%20receivedDateTime%20lt%20" + until.UTC().Format(time.RFC3339) +
		"&$orderby=receivedDateTime%20asc,internetMessageId%20asc&$top=100&$count=true"
}

type workIQWindowAttempt struct {
	binding              workiq.AccountBinding
	since, until         time.Time
	scope, qualification string
	started              time.Time
}

func (s *Store) workIQWindowAttempt(ctx context.Context, attempt int64) (workIQWindowAttempt, error) {
	binding, err := s.validateWorkIQWindowOwner(ctx, attempt)
	if err != nil {
		return workIQWindowAttempt{}, err
	}
	var since, until, started sql.NullString
	w := workIQWindowAttempt{binding: binding}
	if err := s.db.QueryRowContext(ctx, `select window_start,window_end,qualification,started_at from workiq_attempts where id=?`, attempt).
		Scan(&since, &until, &w.qualification, &started); err != nil {
		return workIQWindowAttempt{}, err
	}
	for _, clock := range []struct {
		raw  sql.NullString
		into *time.Time
	}{{since, &w.since}, {until, &w.until}, {started, &w.started}} {
		at, err := workIQStoredTime(clock.raw)
		if err != nil {
			return workIQWindowAttempt{}, err
		}
		if at == nil {
			return workIQWindowAttempt{}, errors.New("a WorkIQ mail window has no fixed range")
		}
		*clock.into = *at
	}
	w.scope = WorkIQWindowScope(w.since, w.until)
	return w, nil
}

// WorkIQWindowProgress is one window position: its frontier and the state of its last page.
type WorkIQWindowProgress struct {
	ID, Revision  int64
	PendingCursor string `json:"-"`
	// Started says a page is staged; Terminal says the last page had no next link, and Delta that it
	// ended in a delta link, which PendingCursor then holds.
	Started, Terminal, Delta bool
}

func (WorkIQWindowProgress) String() string   { return "WorkIQWindowProgress" }
func (WorkIQWindowProgress) GoString() string { return "WorkIQWindowProgress" }

func (s *Store) WorkIQWindowProgressFor(ctx context.Context, attempt int64, resource string) (WorkIQWindowProgress, error) {
	w, err := s.workIQWindowAttempt(ctx, attempt)
	if err != nil {
		return WorkIQWindowProgress{}, err
	}
	if _, err := workiq.RelativePath(resource, ""); err != nil {
		return WorkIQWindowProgress{}, err
	}
	var p WorkIQWindowProgress
	err = s.db.QueryRowContext(ctx, `select id,revision,pending_cursor from workiq_frontiers
where family='mail' and account_id=? and resource=? and scope=? and qualification=? and retired=0`,
		w.binding.Fact.ID, resource, w.scope, w.qualification).Scan(&p.ID, &p.Revision, &p.PendingCursor)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkIQWindowProgress{}, nil
	}
	if err != nil || p.Revision == 0 {
		return p, err
	}
	var next, delta string
	if err := s.db.QueryRowContext(ctx, `select next_cursor,delta_cursor from workiq_pages where frontier_id=? and expected_revision=?`,
		p.ID, p.Revision-1).Scan(&next, &delta); err != nil {
		return WorkIQWindowProgress{}, err
	}
	p.Started, p.Terminal, p.Delta = true, next == "", next == "" && delta != ""
	return p, nil
}

// RetireWorkIQWindowFrontier ends one window position, so the next page starts a new generation:
// a listing walked again or a delta walk the server expired. Its pages and members stay.
func (s *Store) RetireWorkIQWindowFrontier(ctx context.Context, attempt int64, resource string, expected int64) error {
	w, err := s.workIQWindowAttempt(ctx, attempt)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `update workiq_frontiers set retired=1 where family='mail' and account_id=? and resource=? and scope=? and qualification=?
and retired=0 and revision=?`, w.binding.Fact.ID, resource, w.scope, w.qualification, expected)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("WorkIQ window position changed before it was retired")
		}
		return nil
	})
}

// WorkIQWindowPage says what one staged window page held and changed.
type WorkIQWindowPage struct {
	Items, InWindow, OutOfWindow, Removed, Repeats, ClockUnknown int
	// Affecting counts the changes that can change the window's result: in-window additions and
	// changes, removals of live in-window members, and removals or undated items it cannot classify.
	Affecting int
	Bytes     int64
	Cleared   bool
}

type workIQWindowMember struct {
	version, folder, state string
	removed, inWindow      bool
}

// StageWorkIQWindowPage stores one window page and applies its items to the window's members, in
// one transaction with the cursor advance. A page with no live in-window item is stored already
// cleared: its digest, size and message IDs are kept, its body never reaches the archive.
func (s *Store) StageWorkIQWindowPage(ctx context.Context, attempt int64, folder, resource string, expected int64, page workiq.Page, observed time.Time) (WorkIQWindowPage, error) {
	w, err := s.workIQWindowAttempt(ctx, attempt)
	if err != nil {
		return WorkIQWindowPage{}, err
	}
	if observed.IsZero() || folder == "" {
		return WorkIQWindowPage{}, errors.New("invalid WorkIQ window page scope")
	}
	decoded, err := workiq.DecodePage(ctx, page.JSON)
	if err != nil {
		return WorkIQWindowPage{}, err
	}
	path, err := workiq.RelativePath(resource, "")
	if err != nil {
		return WorkIQWindowPage{}, err
	}
	u, _ := url.ParseRequestURI(path)
	base := "/me/mailFolders/" + url.PathEscape(folder) + "/messages"
	if u.EscapedPath() != base && u.EscapedPath() != base+"/delta" {
		return WorkIQWindowPage{}, errors.New("WorkIQ window resource is outside its folder")
	}
	for _, cursor := range []string{decoded.NextCursor, decoded.DeltaCursor} {
		if cursor != "" {
			if _, err := workiq.RelativePath(cursor, u.Path); err != nil {
				return WorkIQWindowPage{}, err
			}
		}
	}
	var data struct {
		Value *[]json.RawMessage `json:"value"`
	}
	if json.Unmarshal(decoded.Data, &data) != nil || data.Value == nil {
		return WorkIQWindowPage{}, errors.New("WorkIQ window page has no value array")
	}
	var items []workiq.MailMessage
	var ids []string
	for _, raw := range *data.Value {
		m, err := workiq.DecodeMail(ctx, raw)
		if err != nil {
			return WorkIQWindowPage{}, err
		}
		if !m.Removed && m.FolderID != folder {
			return WorkIQWindowPage{}, errors.New("WorkIQ mail item is outside its folder")
		}
		items = append(items, m)
		ids = append(ids, m.ID)
	}
	out := WorkIQWindowPage{Items: len(items), Bytes: int64(len(decoded.JSON))}
	inWindow := func(m workiq.MailMessage) bool {
		return m.ReceivedAt != nil && !m.ReceivedAt.Before(w.since) && m.ReceivedAt.Before(w.until)
	}
	keep := false
	for _, m := range items {
		keep = keep || !m.Removed && (m.ReceivedAt == nil || inWindow(m))
	}
	digest := sha256.Sum256(decoded.JSON)
	listed, _ := json.Marshal(ids) // plain strings always encode
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		account, scope, qualification := w.binding.Fact.ID, w.scope, w.qualification
		if _, err := tx.ExecContext(ctx, `insert into workiq_frontiers(family,account_id,resource,scope,qualification,generation)
select 'mail',?,?,?,?,coalesce(max(generation),-1)+1 from workiq_frontiers where family='mail' and account_id=? and resource=? and scope=? and qualification=?
on conflict(family,account_id,resource,scope,qualification) where retired=0 do nothing`,
			account, path, scope, qualification, account, path, scope, qualification); err != nil {
			return err
		}
		var frontier, revision int64
		if err := tx.QueryRowContext(ctx, `select id,revision from workiq_frontiers where family='mail' and account_id=? and resource=? and scope=? and qualification=? and retired=0`,
			account, path, scope, qualification).Scan(&frontier, &revision); err != nil {
			return err
		}
		if revision != expected {
			return errors.New("WorkIQ cursor changed before publication")
		}
		body, cleared := string(decoded.JSON), any(nil)
		if !keep {
			body, cleared, out.Cleared = "", workIQTimeValue(&observed), true
		}
		result, err := tx.ExecContext(ctx, `insert into workiq_pages(attempt_id,frontier_id,expected_revision,data_json,next_cursor,delta_cursor,observed_at,
body_bytes,body_sha256,message_ids_json,cleared_at) values(?,?,?,?,?,?,?,?,?,?,?)`,
			attempt, frontier, revision, body, decoded.NextCursor, decoded.DeltaCursor, workIQTimeValue(&observed),
			len(decoded.JSON), hex.EncodeToString(digest[:]), string(listed), cleared)
		if err != nil {
			return err
		}
		pageID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		for index, m := range items {
			if err := applyWorkIQWindowItem(ctx, tx, w, account, frontier, pageID, index, m, inWindow(m), observed, &out); err != nil {
				return err
			}
		}
		next := decoded.NextCursor
		if next == "" {
			next = decoded.DeltaCursor
		}
		_, err = tx.ExecContext(ctx, `update workiq_frontiers set pending_cursor=?,revision=revision+1 where id=? and revision=?`, next, frontier, revision)
		return err
	})
	if err != nil {
		return WorkIQWindowPage{}, err
	}
	return out, nil
}

func applyWorkIQWindowItem(ctx context.Context, tx *sql.Tx, w workIQWindowAttempt, account string, frontier, pageID int64, index int,
	m workiq.MailMessage, in bool, observed time.Time, out *WorkIQWindowPage) error {
	var old workIQWindowMember
	err := tx.QueryRowContext(ctx, `select coalesce(version,''),folder_id,state,removed,in_window from workiq_window_members where frontier_id=? and graph_id=?`,
		frontier, m.ID).Scan(&old.version, &old.folder, &old.state, &old.removed, &old.inWindow)
	known := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if m.Removed {
		out.Removed++
		// Only a removal of a live in-window member, or of an ID this window never saw, can change it.
		if !known || !old.removed && old.inWindow {
			out.Affecting++
		}
		if known && old.removed {
			return nil
		}
		folder := old.folder
		if !known {
			folder = "" // a removal names no folder
		}
		_, err := tx.ExecContext(ctx, `insert into workiq_window_members(frontier_id,graph_id,account_id,folder_id,in_window,removed,page_id,item_index,state,observed_at)
values(?,?,?,?,0,1,?,?,'removed',?) on conflict(frontier_id,graph_id) do update set removed=1,state='removed',page_id=excluded.page_id,
item_index=excluded.item_index,observed_at=excluded.observed_at`, frontier, m.ID, account, folder, pageID, index, workIQTimeValue(&observed))
		return err
	}
	version := ""
	if m.ModifiedAt != nil {
		version = m.ModifiedAt.UTC().Format(time.RFC3339Nano)
	}
	switch {
	case m.ReceivedAt == nil:
		out.ClockUnknown++
	case in:
		out.InWindow++
	default:
		out.OutOfWindow++
	}
	if known && !old.removed && old.folder == m.FolderID && !workIQWindowNewer(version, old.version) {
		out.Repeats++ // an exact repeat or an older version: nothing changes
		return nil
	}
	if in || m.ReceivedAt == nil {
		out.Affecting++
	}
	_, err = tx.ExecContext(ctx, `insert into workiq_window_members(frontier_id,graph_id,account_id,folder_id,version,received_at,in_window,clock_unknown,removed,
page_id,item_index,state,observed_at) values(?,?,?,?,?,?,?,?,0,?,?,'staged',?) on conflict(frontier_id,graph_id) do update set
folder_id=excluded.folder_id,version=excluded.version,received_at=excluded.received_at,in_window=excluded.in_window,clock_unknown=excluded.clock_unknown,
removed=0,page_id=excluded.page_id,item_index=excluded.item_index,state='staged',observed_at=excluded.observed_at`,
		frontier, m.ID, account, m.FolderID, nullText(version), workIQTimeValue(m.ReceivedAt), b2i(in), b2i(m.ReceivedAt == nil), pageID, index, workIQTimeValue(&observed))
	return err
}

// workIQWindowNewer says version is a different, not older, server version than old; an unknown
// clock on either side counts as different, as nothing shows it is the same.
func workIQWindowNewer(version, old string) bool {
	if version == old && version != "" {
		return false
	}
	a, errA := time.Parse(time.RFC3339Nano, version)
	b, errB := time.Parse(time.RFC3339Nano, old)
	return errA != nil || errB != nil || a.After(b)
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// EnsureWorkIQWindowFolders gives each listed folder its archive folder rows when it has none, so
// admitted mail has a folder. Existing rows, and the whole-account inventory they belong to, are
// left exactly as they are.
func (s *Store) EnsureWorkIQWindowFolders(ctx context.Context, attempt int64, folders []workiq.Folder) error {
	w, err := s.workIQWindowAttempt(ctx, attempt)
	if err != nil {
		return err
	}
	account := w.binding.Fact.ID
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, f := range folders {
			kind := f.WellKnown
			switch kind {
			case "":
				kind = "other"
			case "other", "inbox", "sent", "deleted", "drafts", "archive", "junk":
			default:
				return errors.New("invalid fixed WorkIQ folder kind")
			}
			if f.ID == "" || f.ID == f.ParentID {
				return errors.New("invalid WorkIQ mail folder scope")
			}
			key, err := mailSourceKey(ctx, tx, account, "workiq:folder", f.ID)
			if err != nil {
				return err
			}
			var parent uint32
			if f.ParentID != "" {
				if parent, err = mailSourceKey(ctx, tx, account, "workiq:folder", f.ParentID); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `insert into workiq_folders(account_id,graph_id,folder_key,parent_graph_id,well_known,inventory_attempt,total_items,child_count)
values(?,?,?,?,?,0,?,?) on conflict(account_id,graph_id) do nothing`, account, f.ID, key, f.ParentID, kind, f.TotalCount, f.ChildCount); err != nil {
				return err
			}
			raw, _ := json.Marshal(struct{ ID, ParentID, Name string }{f.ID, f.ParentID, f.Name}) // plain strings always encode
			safe, _ := scrubWorkIQProjection(raw)
			var cleaned struct{ ID, ParentID, Name string }
			if json.Unmarshal(safe, &cleaned) != nil || cleaned.ID != f.ID || cleaned.ParentID != f.ParentID {
				return errors.New("WorkIQ folder identity could not be retained")
			}
			if _, err := tx.ExecContext(ctx, `insert into mail_folders(account,folder_key,parent_key,name,kind,modified_at) values(?,?,?,?,?,?)
on conflict(account,folder_key) do nothing`, account, key, parent, cleaned.Name, kind, nowText()); err != nil {
				return err
			}
		}
		return nil
	})
}

// WorkIQWindowAdmission is what one bounded admission call did.
type WorkIQWindowAdmission struct {
	Admitted, Updated, Deferred, Resolved, Cleared int
	Gaps                                           map[string]int
	Done                                           bool
}

// workIQWindowBlocking is a member that still needs its page body: a deferred in-window item, or a
// live in-window or undated item its position may still admit. A retired position admits nothing;
// its next walk fetches those items again, so only its deferred items keep their bodies.
const workIQWindowBlocking = `m.removed=0 and (m.in_window=1 or m.clock_unknown=1) and (m.state='deferred' or m.state='staged' and f.retired=0)`

// AdmitWorkIQWindow admits at most limit staged in-window members of the window's positions for
// the given resources: by Graph ID into the row it is bound to, deferred when the archive already
// holds a matching row, or as a new server-only row marked identity_unproven (§1 of the design).
// Once none is left it resolves the account's deferred members whose Graph ID is now bound, or
// that a later complete whole-account census has passed, and clears every window page body no
// member still needs. Done says nothing is left.
func (s *Store) AdmitWorkIQWindow(ctx context.Context, attempt int64, resources []string, limit int) (WorkIQWindowAdmission, error) {
	w, err := s.workIQWindowAttempt(ctx, attempt)
	if err != nil {
		return WorkIQWindowAdmission{}, err
	}
	if limit < 1 || limit > 128 {
		return WorkIQWindowAdmission{}, errors.New("invalid bounded window admission")
	}
	account := w.binding.Fact.ID
	out := WorkIQWindowAdmission{Gaps: map[string]int{}}
	resourceJSON, _ := json.Marshal(resources) // plain strings always encode
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		type member struct {
			frontier, page int64
			graph          string
			index          int
			observed       string
		}
		var batch []member
		if err := eachRow(ctx, tx, `select m.frontier_id,m.graph_id,coalesce(m.page_id,0),m.item_index,m.observed_at from workiq_window_members m
join workiq_frontiers f on f.id=m.frontier_id where f.family='mail' and f.account_id=? and f.scope=? and f.qualification=? and f.retired=0
and f.resource in (select value from json_each(?)) and m.state='staged' and m.in_window=1 and m.removed=0 and m.clock_unknown=0
order by m.frontier_id,m.graph_id limit ?`, []any{account, w.scope, w.qualification, string(resourceJSON), limit}, func(row *sql.Rows) error {
			var item member
			if err := row.Scan(&item.frontier, &item.graph, &item.page, &item.index, &item.observed); err != nil {
				return err
			}
			batch = append(batch, item)
			return nil
		}); err != nil {
			return err
		}
		for _, item := range batch {
			server, err := loadWorkIQWindowItem(ctx, tx, item.page, item.index, item.graph)
			if err != nil {
				return err
			}
			observed, err := time.Parse(time.RFC3339Nano, item.observed)
			if err != nil {
				return errors.New("invalid WorkIQ mail observation clock")
			}
			state, match, err := admitWorkIQWindowItem(ctx, tx, account, server, observed, &out)
			if err != nil {
				return err
			}
			version := ""
			if server.ModifiedAt != nil {
				version = server.ModifiedAt.UTC().Format(time.RFC3339Nano)
			}
			if _, err := tx.ExecContext(ctx, `update workiq_window_members set state=?,admitted_version=?,match_rowid=? where frontier_id=? and graph_id=?`,
				state, nullText(version), match, item.frontier, item.graph); err != nil {
				return err
			}
		}
		if len(batch) == limit {
			return nil
		}
		resolved, err := tx.ExecContext(ctx, `update workiq_window_members set state='resolved' where account_id=? and state='deferred' and (
exists(select 1 from workiq_aliases a where a.kind='mail' and a.account_id=workiq_window_members.account_id and a.namespace='graph' and a.alias=workiq_window_members.graph_id)
or exists(select 1 from workiq_attempts a join workiq_censuses c on c.attempt_id=a.id where a.family='mail' and a.kind='sync' and a.account_id=workiq_window_members.account_id
and a.status in ('ok','ok_with_omissions') and c.complete=1 and a.started_at>workiq_window_members.observed_at))`, account)
		if err != nil {
			return err
		}
		n, err := resolved.RowsAffected()
		if err != nil {
			return err
		}
		out.Resolved = int(n)
		at := time.Now()
		cleared, err := tx.ExecContext(ctx, `update workiq_pages set data_json='',cleared_at=? where id in (
select p.id from workiq_pages p join workiq_frontiers f on f.id=p.frontier_id
where f.family='mail' and f.account_id=? and f.scope like 'window:%' and p.cleared_at is null and p.body_sha256 is not null
and not exists(select 1 from workiq_window_members m where m.page_id=p.id and `+workIQWindowBlocking+`)
order by p.id limit ?)`, workIQTimeValue(&at), account, limit)
		if err != nil {
			return err
		}
		if n, err = cleared.RowsAffected(); err != nil {
			return err
		}
		out.Cleared = int(n)
		out.Done = out.Cleared < limit
		return nil
	})
	if err != nil {
		return WorkIQWindowAdmission{}, err
	}
	return out, nil
}

func loadWorkIQWindowItem(ctx context.Context, tx *sql.Tx, page int64, index int, graph string) (workiq.MailMessage, error) {
	var body string
	if err := tx.QueryRowContext(ctx, `select data_json from workiq_pages where id=?`, page).Scan(&body); err != nil {
		return workiq.MailMessage{}, err
	}
	if body == "" {
		return workiq.MailMessage{}, errors.New("a WorkIQ window page was cleared before its in-window mail was admitted")
	}
	decoded, err := workiq.DecodePage(ctx, []byte(body))
	if err != nil {
		return workiq.MailMessage{}, err
	}
	var data struct {
		Value []json.RawMessage `json:"value"`
	}
	if json.Unmarshal(decoded.Data, &data) != nil || index < 0 || index >= len(data.Value) {
		return workiq.MailMessage{}, errors.New("a WorkIQ window member does not match its staged page")
	}
	m, err := workiq.DecodeMail(ctx, data.Value[index])
	if err != nil {
		return workiq.MailMessage{}, err
	}
	if m.ID != graph || m.Removed {
		return workiq.MailMessage{}, errors.New("a WorkIQ window member does not match its staged page")
	}
	return m, nil
}

// admitWorkIQWindowItem decides one in-window item and returns its member state and, when deferred,
// the row it matched.
func admitWorkIQWindowItem(ctx context.Context, tx *sql.Tx, account string, server workiq.MailMessage, observed time.Time, out *WorkIQWindowAdmission) (string, any, error) {
	var bound bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_aliases where kind='mail' and account_id=? and namespace='graph' and alias=?)`,
		account, server.ID).Scan(&bound); err != nil {
		return "", nil, err
	}
	normalized := workiq.NormalizeMessageID(server.InternetMessageID)
	if !bound && normalized != "" {
		candidates, err := workIQNativeMailCandidates(ctx, tx, account, normalized)
		if err != nil {
			return "", nil, err
		}
		for _, id := range candidates {
			var header, subject, sender string
			var sent sql.NullString
			if err := tx.QueryRowContext(ctx, `select internet_message_id,subject,sender_address,sent_at from mail_messages where rowid=?`, id).
				Scan(&header, &subject, &sender, &sent); err != nil {
				return "", nil, err
			}
			at, err := workIQStoredTime(sent)
			if err != nil {
				return "", nil, err
			}
			local := workiq.MailMessage{InternetMessageID: header, Subject: subject, Sender: workiq.MailAddress{Address: sender}, SentAt: at}
			if workiq.MailCorroborates(local, server) {
				out.Deferred++
				return "deferred", id, nil
			}
		}
		if len(candidates) > 0 {
			out.Gaps["workiq_mail_identity_uncorroborated"]++
		}
	}
	result := WorkIQMailResult{Gaps: map[string]int{}}
	if err := commitWorkIQMailItem(ctx, tx, account, server, observed, 1, &result, workIQMailCommit{window: true}); err != nil {
		return "", nil, err
	}
	out.Admitted += result.Added
	out.Updated += result.Updated
	for code, n := range result.Gaps {
		out.Gaps[code] += n
	}
	return "admitted", nil, nil
}

// WorkIQWindowFacts is what one window position holds, for the disk check.
type WorkIQWindowFacts struct {
	InWindow, OutOfWindow, StagedIn, StagedBytes, LargestPage, ClockUnknown int64
	// Unadmitted counts live in-window members still waiting for admission (not deferred ones).
	Unadmitted int64
}

func (s *Store) WorkIQWindowFactsFor(ctx context.Context, attempt int64, resource string) (WorkIQWindowFacts, error) {
	w, err := s.workIQWindowAttempt(ctx, attempt)
	if err != nil {
		return WorkIQWindowFacts{}, err
	}
	var f WorkIQWindowFacts
	err = s.db.QueryRowContext(ctx, `with fr as (select id from workiq_frontiers where family='mail' and account_id=? and resource=? and scope=? and qualification=? and retired=0)
select coalesce((select sum(in_window) from workiq_window_members where frontier_id in fr and removed=0),0),
coalesce((select sum(1-in_window) from workiq_window_members where frontier_id in fr and removed=0 and clock_unknown=0),0),
coalesce((select count(*) from workiq_window_members where frontier_id in fr and removed=0 and in_window=1 and state in ('staged','deferred')),0),
coalesce((select sum(length(cast(data_json as blob))) from workiq_pages where frontier_id in fr),0),
coalesce((select max(body_bytes) from workiq_pages where frontier_id in fr),0),
coalesce((select count(*) from workiq_window_members where frontier_id in fr and removed=0 and clock_unknown=1),0),
coalesce((select count(*) from workiq_window_members where frontier_id in fr and removed=0 and in_window=1 and clock_unknown=0 and state='staged'),0)`,
		w.binding.Fact.ID, resource, w.scope, w.qualification).Scan(&f.InWindow, &f.OutOfWindow, &f.StagedIn, &f.StagedBytes, &f.LargestPage, &f.ClockUnknown, &f.Unadmitted)
	return f, err
}

// RecordWorkIQWindowCoverage records the folders a window proved, and all-folder coverage when
// every folder was proven. It refuses while any in-window member of those folders is unadmitted.
func (s *Store) RecordWorkIQWindowCoverage(ctx context.Context, attempt int64, folders []string, all bool, snapshot time.Time) error {
	w, err := s.workIQWindowAttempt(ctx, attempt)
	if err != nil {
		return err
	}
	if snapshot.IsZero() || snapshot.Before(w.until) {
		return errors.New("a WorkIQ window snapshot must end after the window")
	}
	account := w.binding.Fact.ID
	return s.inTx(ctx, func(tx *sql.Tx) error {
		total := 0
		for _, folder := range folders {
			var pending, deferred int
			if err := tx.QueryRowContext(ctx, `select coalesce(sum(m.state='staged' or m.clock_unknown=1),0),coalesce(sum(m.state='deferred'),0)
from workiq_window_members m join workiq_frontiers f on f.id=m.frontier_id
where f.family='mail' and f.account_id=? and f.scope=? and f.qualification=? and f.retired=0 and f.resource=?
and m.removed=0 and (m.in_window=1 or m.clock_unknown=1)`, account, w.scope, w.qualification, WorkIQWindowDeltaResource(folder, w.since)).Scan(&pending, &deferred); err != nil {
				return err
			}
			if pending != 0 {
				return errors.New("a WorkIQ window folder still has unadmitted mail")
			}
			total += deferred
			if _, err := tx.ExecContext(ctx, `insert into workiq_window_coverage(attempt_id,account_id,folder_id,since,until,snapshot_at,deferred) values(?,?,?,?,?,?,?)`,
				attempt, account, folder, workIQTimeValue(&w.since), workIQTimeValue(&w.until), workIQTimeValue(&snapshot), deferred); err != nil {
				return err
			}
		}
		if !all {
			return nil
		}
		_, err := tx.ExecContext(ctx, `insert into workiq_window_coverage(attempt_id,account_id,folder_id,since,until,snapshot_at,deferred) values(?,?,'',?,?,?,?)`,
			attempt, account, workIQTimeValue(&w.since), workIQTimeValue(&w.until), workIQTimeValue(&snapshot), total)
		return err
	})
}
