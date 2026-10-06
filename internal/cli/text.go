package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/ourostack/teamscrawl/internal/render"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/syncer"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// displayZone is the zone text output shows times in; tests pin it.
var displayZone = time.Local

func stamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(displayZone).Format("2006-01-02 15:04")
}

func metaLines(w io.Writer, m meta, color bool) {
	if m.ArchiveAgeSeconds != nil {
		_, _ = fmt.Fprintf(w, "%s\n", render.Dim("archive age: "+(time.Duration(*m.ArchiveAgeSeconds)*time.Second).String(), color))
	} else {
		_, _ = fmt.Fprintf(w, "%s\n", render.Dim("archive age: never synced", color))
	}
	if m.SyncError != nil {
		_, _ = fmt.Fprintf(w, "sync error: %s: %s\n", m.SyncError.Code, m.SyncError.Message)
	}
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// textBanners are the commands whose text output opens with the wordmark.
var textBanners = map[string]bool{"doctor": true, "status": true, "sync": true, "whoami": true}

// renderText prints a result for a person, in color when rt.color is set.
func (rt *runtime) renderText(label string, v any) error {
	w, color := rt.stdout, rt.color
	if textBanners[label] {
		render.Banner(w, label, color)
	}
	switch r := v.(type) {
	case *listResult:
		rt.listTable(r)
		_, _ = fmt.Fprintln(w)
		if r.CoverageGap != nil && *r.CoverageGap {
			_, _ = fmt.Fprintf(w, "%s\n", render.Dim(uncoveredNote(r), color))
		}
		if len(r.UnlinkedAccounts) > 0 {
			_, _ = fmt.Fprintf(w, "%s\n", render.Dim("events of these outlook accounts are not merged with a Teams account: "+strings.Join(r.UnlinkedAccounts, ", "), color))
		}
		if len(r.UnlinkedRecaps) > 0 {
			rt.unlinkedRecapTable(r)
		}
		more, count := "", strconv.Itoa(r.Count)
		if r.Truncated {
			more = " (more exist; raise --limit)"
			if r.Total > 0 {
				count += " of " + strconv.Itoa(r.Total)
			}
		}
		_, _ = fmt.Fprintf(w, "%s\n", render.Dim(fmt.Sprintf("%s items%s", count, more), color))
		metaLines(w, r.meta, color)
	case syncer.Report:
		render.Block(w, "Sync", map[string]any{"status": r.Status}, color)
		_, _ = fmt.Fprintln(w)
		rows := [][]string{}
		for _, p := range []struct {
			n string
			c store.Counts
		}{{"conversations", r.Conversations}, {"messages", r.Messages}, {"people", r.People}, {"activity", r.Activity}} {
			rows = append(rows, []string{p.n, strconv.Itoa(p.c.Seen), strconv.Itoa(p.c.Inserted), strconv.Itoa(p.c.Updated), strconv.Itoa(p.c.Unchanged)})
		}
		for _, p := range []struct {
			n string
			c store.Counts
		}{{"calendar events", r.Calendar.Events}, {"calendar recaps", r.Calendar.Recaps}, {"calendar recap items", r.Calendar.RecapItems}} {
			rows = append(rows, []string{p.n, strconv.Itoa(p.c.Seen), strconv.Itoa(p.c.Inserted), strconv.Itoa(p.c.Updated), strconv.Itoa(p.c.Unchanged)})
		}
		render.Table(w, []string{"kind", "seen", "inserted", "updated", "unchanged"}, rows, color)
		if m := r.Migrated; m != nil {
			_, _ = fmt.Fprintf(w, "migrated: %d rows re-derived from stored records (derivation %d to %d); not counted as updates\n", m.Rows, m.From, m.To)
		}
		keys := make([]string, 0, len(r.Omissions))
		for k := range r.Omissions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			_, _ = fmt.Fprintf(w, "omitted %s: %d\n", k, r.Omissions[k])
		}
		if r.Redacted > 0 {
			_, _ = fmt.Fprintf(w, "redacted: %d credential-looking values in generic records\n", r.Redacted)
		}
		for _, src := range r.Sources {
			if src.Error != nil {
				_, _ = fmt.Fprintf(w, "failed %s: %s: %s\n", src.Source, src.Error.Code, src.Error.Message)
			}
		}
	case *statusResult:
		rt.statusBlock("Status", r)
		metaLines(w, r.meta, color)
	case *eventResult:
		rt.eventBlock(r)
	case *whoamiResult:
		rows := [][]string{}
		for _, a := range r.Accounts {
			name := a.DisplayName
			if name == "" {
				name = "(name not seen yet)"
			}
			rows = append(rows, []string{name, a.TenantID, a.UserID, stamp(a.LastSyncedAt)})
		}
		if len(rows) == 0 {
			_, _ = fmt.Fprintln(w, "no accounts archived yet; run teamscrawl sync")
		} else {
			render.Table(w, []string{"name", "tenant", "user", "last synced"}, rows, color)
			_, _ = fmt.Fprintln(w)
		}
		rt.statusBlock("Archive", r.Archive)
		metaLines(w, r.meta, color)
	case *sqlResult:
		rows := make([][]string, len(r.Rows))
		for i, row := range r.Rows {
			rows[i] = make([]string, len(row))
			for j, c := range row {
				rows[i][j] = oneLine(fmt.Sprint(c))
			}
		}
		render.Table(w, r.Columns, rows, color)
		metaLines(w, r.meta, color)
	case *doctorResult:
		checks := make([]render.Check, len(r.Checks))
		for i, c := range r.Checks {
			st := render.OK
			switch {
			case !c.OK:
				st = render.Fail
			case c.Warn:
				st = render.Warn
			}
			checks[i] = render.Check{Name: c.Name, Detail: c.Detail, Fix: c.Fix, Status: st}
		}
		render.Doctor(w, "Doctor", checks, r.snap, color)
	default:
		render.Block(w, label, v, color)
	}
	return nil
}

// statusBlock prints the archive summary: a key/value block and a per-account table.
func (rt *runtime) statusBlock(title string, r *statusResult) {
	w := rt.stdout
	if !r.ArchiveExists {
		render.Block(w, title, map[string]any{"archive_path": r.ArchivePath, "archive_exists": false}, rt.color)
		_, _ = fmt.Fprintln(w, "run teamscrawl sync to create it")
		return
	}
	m := map[string]any{"archive_path": r.ArchivePath, "schema_version": r.SchemaVersion, "fts_present": r.FTSPresent}
	if r.LastRun != nil {
		m["last_run"] = r.LastRun.Status + " at " + stamp(r.LastRun.FinishedAt)
	}
	if len(r.OtherOrigins) > 0 {
		m["other_origins"] = strings.Join(r.OtherOrigins, ", ")
	}
	render.Block(w, title, m, rt.color)
	if len(r.Accounts) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w)
	rows := make([][]string, len(r.Accounts))
	for i, a := range r.Accounts {
		rows[i] = []string{a.TenantID + "/" + a.UserID, strconv.Itoa(a.Conversations), strconv.Itoa(a.Messages), strconv.Itoa(a.People), strconv.Itoa(a.Activity), stamp(a.NewestSentAt)}
	}
	render.Table(w, []string{"account", "conversations", "messages", "people", "activity", "newest"}, rows, rt.color)
}

// databaseLabel splits a database name for the text tables: the manager label without the account
// ("Teams:calendar-manager") and the account as shortened tenant/user ids. A name that carries no
// account is its own label with "-" as the account.
func databaseLabel(name string) (label, account string) {
	manager, acct, ok := teamsdesktop.ParseDatabaseName(name)
	if !ok {
		return name, "-"
	}
	return "Teams:" + manager, shortID(acct.TenantID) + "/" + shortID(acct.UserID)
}

// listTable prints a list result as an aligned table with a column subset per item type; the
// free-text column is clipped so a row fits the terminal.
func (rt *runtime) listTable(r *listResult) {
	var cols []string
	var rows [][]string
	textCol := -1
	for _, it := range r.Items {
		switch x := it.(type) {
		case messageItem:
			cols, textCol = []string{"sent_at", "conversation", "sender", "text"}, 3
			text := oneLine(x.Text)
			if !x.DeletedAt.IsZero() {
				text += " (deleted)"
			}
			rows = append(rows, []string{stamp(x.SentAt), x.ConversationDisplayName, x.SenderName, text})
		case conversationItem:
			cols, textCol = []string{"last_message_at", "kind", "name", "members"}, -1
			rows = append(rows, []string{stamp(x.LastMessageAt), x.Kind, x.DisplayName, strconv.Itoa(x.MemberCount)})
		case teamItem:
			cols, textCol = []string{"last_activity_at", "team", "channels", "unread"}, -1
			rows = append(rows, []string{stamp(x.LastActivityAt), x.DisplayName, strconv.Itoa(x.ChannelCount), strconv.Itoa(x.UnreadCount)})
		case personItem:
			cols, textCol = []string{"name", "id", "last_seen_at"}, -1
			rows = append(rows, []string{x.DisplayName, x.ID, stamp(x.LastSeenAt)})
		case activityItem:
			read := "unread"
			if x.IsRead {
				read = "read"
			}
			cols, textCol = []string{"at", "type", "state", "actor", "sender", "conversation", "text"}, 6
			rows = append(rows, []string{stamp(x.At), x.Type, read, x.ActorName, x.SenderName, x.ConversationDisplayName, oneLine(x.Text)})
		case calendarItem:
			cols, textCol = []string{"start", "end", "status", "subject", "where", "response"}, 3
			start, end := stamp(x.Start), stamp(x.End)
			if x.AllDay != nil && *x.AllDay {
				start, end = x.StartDate, "all day"
			}
			rows = append(rows, []string{start, end, x.Status, oneLine(x.Subject), calendarWhere(x), x.Response})
		case calendarSourceItem:
			cols, textCol = []string{"source", "account", "status", "window", "covered_days", "events", "with_detail", "recaps"}, -1
			rows = append(rows, []string{x.Source, shortAccount(x.AccountID), x.Status, sourceWindow(x), strconv.Itoa(x.CoveredDays), strconv.Itoa(x.EventsLive), strconv.Itoa(x.EventsWithDetail), strconv.Itoa(x.RecapsWithContent)})
		case actionItem:
			cols, textCol = []string{"event_start", "subject", "owner", "title"}, 3
			title := oneLine(x.Title)
			if title == "" {
				title = oneLine(x.Text)
			}
			if x.SeriesLevel {
				title += " (series level)"
			}
			rows = append(rows, []string{stamp(x.EventStart), oneLine(x.Subject), x.Owner, title})
		case storeItem:
			label, acct := databaseLabel(x.Database)
			cols, textCol = []string{"database", "account", "store", "records", "removed", "last_updated_at"}, -1
			rows = append(rows, []string{label, acct, x.Store, strconv.Itoa(x.Records), strconv.Itoa(x.Removed), stamp(x.LastUpdatedAt)})
		case recordItem:
			label, acct := databaseLabel(x.Database)
			cols, textCol = []string{"updated_at", "database", "account", "store", "key", "value"}, 5
			value := oneLine(string(x.ValueJSON))
			if !x.RemovedAt.IsZero() {
				value = "(removed) " + value // first, so the clipped value column keeps it
			}
			rows = append(rows, []string{stamp(x.UpdatedAt), label, acct, x.Store, oneLine(string(x.KeyJSON)), value})
		case projected:
			cols, textCol = x.keys, -1
			row := make([]string, len(x.keys))
			for i, k := range x.keys {
				var v any
				_ = json.Unmarshal(x.vals[k], &v)
				if v != nil {
					row[i] = oneLine(fmt.Sprint(v))
				}
				if k == "text" {
					textCol = i
				}
			}
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		render.Table(rt.stdout, []string{"items"}, nil, rt.color)
		return
	}
	for _, row := range rows {
		for i := range row {
			if i != textCol && cols[i] != "database" {
				row[i] = render.Truncate(row[i], 40)
			}
		}
	}
	if textCol >= 0 {
		used := 2 * (len(cols) - 1)
		for i := range cols {
			if i == textCol {
				continue
			}
			w := runewidth.StringWidth(cols[i])
			for _, row := range rows {
				w = max(w, runewidth.StringWidth(row[i]))
			}
			used += w
		}
		room := max(rt.termWidth()-used, 20)
		for _, row := range rows {
			row[textCol] = render.Truncate(row[textCol], room)
		}
	}
	render.Table(rt.stdout, cols, rows, rt.color)
}

// doctorSnapshot summarizes the archive for the doctor screen; nil when there is no archive.
func (rt *runtime) doctorSnapshot() *render.Snapshot {
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if err != nil {
		return nil
	}
	defer func() { _ = st.Close() }()
	row, err := st.Status(rt.ctx)
	if err != nil {
		return nil
	}
	var conv, msgs, people, act int
	for _, a := range row.Accounts {
		conv, msgs, people, act = conv+a.Conversations, msgs+a.Messages, people+a.People, act+a.Activity
	}
	snap := &render.Snapshot{Pairs: [][2]string{
		{"accounts", strconv.Itoa(len(row.Accounts))}, {"conversations", strconv.Itoa(conv)}, {"messages", strconv.Itoa(msgs)},
		{"people", strconv.Itoa(people)}, {"activity", strconv.Itoa(act)},
	}}
	age := "never synced"
	if !row.LastSuccessAt.IsZero() {
		age = max(rt.now().Sub(row.LastSuccessAt), 0).Round(time.Second).String()
	}
	snap.Lines = [][2]string{{"last sync", stamp(row.LastSuccessAt)}, {"archive age", age}}
	return snap
}

// unlinkedRecapTable lists the recaps that belong to no event, with their action items, so a
// meeting that has no calendar entry is still readable.
func (rt *runtime) unlinkedRecapTable(r *listResult) {
	w, color := rt.stdout, rt.color
	_, _ = fmt.Fprintf(w, "\n%s\n", render.Dim("recaps with no event in the archive", color))
	rows := make([][]string, len(r.UnlinkedRecaps))
	for i, u := range r.UnlinkedRecaps {
		var actions []string
		for _, a := range u.ActionItems {
			actions = append(actions, oneLine(firstOf(a.Title, a.Text)))
		}
		when := stamp(u.PlacedAt)
		if u.PlacedBy == "recording_start" {
			when += " (recording start; meeting start unknown)"
		}
		rows[i] = []string{when, firstOf(oneLine(firstOf(u.Headline, u.ShortSummary)), "(no summary text)"), strings.Join(actions, "; ")}
	}
	render.Table(w, []string{"started", "summary", "action items"}, rows, color)
	if r.UnlinkedRecapsTotal > len(r.UnlinkedRecaps) {
		_, _ = fmt.Fprintf(w, "%s\n", render.Dim(fmt.Sprintf("%d of %d recaps shown; raise --limit", len(r.UnlinkedRecaps), r.UnlinkedRecapsTotal), color))
	}
}

// shortAccount shortens the ids of a "tenant/user" account as the database tables do; any other
// account name is shown as it is.
func shortAccount(account string) string {
	tenant, user, ok := strings.Cut(account, "/")
	if !ok || strings.HasPrefix(account, "outlook/") {
		return account
	}
	return shortID(tenant) + "/" + shortID(user)
}

func shortID(id string) string {
	if len(id) > 9 {
		return id[:4] + "…" + id[len(id)-4:]
	}
	return id
}

// sourceWindow is the first and last covered day of a source, or "-" when it has none.
func sourceWindow(x calendarSourceItem) string {
	if x.WindowStart.IsZero() {
		return "-"
	}
	return x.WindowStart.Format("2006-01-02") + ".." + x.WindowEnd.Format("2006-01-02")
}

// calendarWhere is the first room of an event, or "online" for a meeting with no room.
func calendarWhere(x calendarItem) string {
	if len(x.Rooms) > 0 {
		return x.Rooms[0].Name
	}
	if x.IsOnlineMeeting != nil && *x.IsOnlineMeeting || x.JoinURL != "" {
		return "online"
	}
	return ""
}

// eventBlock prints one event for a person: the headline fields, then attendees, action items and
// the recap text.
func (rt *runtime) eventBlock(r *eventResult) {
	w, color, ev := rt.stdout, rt.color, r.event
	if ev == nil {
		_, _ = fmt.Fprintln(w, "no calendar event to show: the archive has no calendar yet; run teamscrawl sync")
		metaLines(w, r.meta, color)
		return
	}
	when := stamp(ev.Start) + " to " + stamp(ev.End)
	if ev.AllDay != nil && *ev.AllDay {
		when = ev.StartDate + " (all day)"
	}
	head := map[string]any{"when": when, "status": ev.Status, "detail": ev.DetailLevel}
	set := func(k, v string) {
		if v != "" {
			head[k] = v
		}
	}
	set("response", ev.Response)
	set("organizer", ev.OrganizerName)
	set("join link", ev.JoinURL)
	set("location", ev.Location)
	set("event id", ev.EventID)
	if len(ev.Rooms) > 0 {
		names := make([]string, len(ev.Rooms))
		for i, rm := range ev.Rooms {
			names[i] = rm.Name
		}
		set("rooms", strings.Join(names, ", "))
	}
	if ev.Chat != nil {
		set("chat", fmt.Sprintf("%s (%d messages)", ev.Chat.DisplayName, ev.Chat.MessageCount))
	}
	render.Block(w, oneLine(ev.Subject), head, color)
	if len(ev.UnknownFields) > 0 {
		_, _ = fmt.Fprintf(w, "%s\n", render.Dim("unknown: "+strings.Join(ev.UnknownFields, ", "), color))
	}
	if ev.DetailLevel == "stale" {
		_, _ = fmt.Fprintf(w, "%s\n", render.Dim("attendees as of "+stamp(ev.DetailAsOf)+"; the event changed since", color))
	}
	if len(ev.Attendees) > 0 {
		_, _ = fmt.Fprintln(w)
		rows := make([][]string, len(ev.Attendees))
		for i, a := range ev.Attendees {
			rows[i] = []string{a.Name, a.Role, a.Response}
		}
		render.Table(w, []string{"attendee", "role", "response"}, rows, color)
	}
	var actions [][]string
	for _, rc := range ev.Recaps {
		for _, a := range rc.ActionItems {
			actions = append(actions, []string{a.Owner, oneLine(firstOf(a.Title, a.Text))})
		}
	}
	if len(actions) > 0 {
		_, _ = fmt.Fprintln(w)
		render.Table(w, []string{"owner", "action item"}, actions, color)
	}
	for _, rc := range ev.Recaps {
		for _, text := range []string{rc.ShortSummary, rc.Outline} {
			if text != "" {
				_, _ = fmt.Fprintf(w, "\n%s\n", wrapText(text, rt.termWidth()))
			}
		}
	}
	if len(ev.Recordings) > 0 {
		_, _ = fmt.Fprintln(w)
		rows := make([][]string, len(ev.Recordings))
		for i, rec := range ev.Recordings {
			rows[i] = []string{stamp(rec.SentAt), rec.Kind, rec.MatchedBy}
		}
		render.Table(w, []string{"sent_at", "recording", "matched by"}, rows, color)
	}
	metaLines(w, r.meta, color)
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// wrapText breaks s into lines of at most width cells at word boundaries; newlines in s stay.
func wrapText(s string, width int) string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			if line != "" && runewidth.StringWidth(line)+1+runewidth.StringWidth(word) > width {
				out = append(out, line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// uncoveredNote says which days of a calendar range no cached data covers, so an absent event on
// one of them is not read as "nothing happened".
func uncoveredNote(r *listResult) string {
	n := r.UncoveredDaysTotal
	if n == 0 {
		n = len(r.UncoveredDays)
	}
	switch {
	case n == 0:
		return "no cached data covers part of this range"
	case n <= 5 && len(r.UncoveredDays) == n:
		return "no cached data covers " + strconv.Itoa(n) + " day(s) of this range: " + strings.Join(r.UncoveredDays, ", ")
	}
	return "no cached data covers " + strconv.Itoa(n) + " days of this range, from " + r.UncoveredDays[0]
}
