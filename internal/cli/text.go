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
		for _, src := range r.Sources {
			if src.Error != nil {
				_, _ = fmt.Fprintf(w, "failed %s: %s: %s\n", src.Source, src.Error.Code, src.Error.Message)
			}
		}
	case *statusResult:
		rt.statusBlock("Status", r)
		metaLines(w, r.meta, color)
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
		case personItem:
			cols, textCol = []string{"name", "id", "last_seen_at"}, -1
			rows = append(rows, []string{x.DisplayName, x.ID, stamp(x.LastSeenAt)})
		case activityItem:
			read := "unread"
			if x.IsRead {
				read = "read"
			}
			cols, textCol = []string{"at", "type", "state", "sender", "conversation", "text"}, 5
			rows = append(rows, []string{stamp(x.At), x.Type, read, x.SenderName, x.ConversationDisplayName, oneLine(x.Text)})
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
			if i != textCol {
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
