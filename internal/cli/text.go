package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/syncer"
)

func stamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func metaLines(w io.Writer, m meta) {
	if m.ArchiveAgeSeconds != nil {
		_, _ = fmt.Fprintf(w, "archive age: %s\n", (time.Duration(*m.ArchiveAgeSeconds) * time.Second).String())
	} else {
		_, _ = fmt.Fprintln(w, "archive age: never synced")
	}
	if m.SyncError != nil {
		_, _ = fmt.Fprintf(w, "sync error: %s: %s\n", m.SyncError.Code, m.SyncError.Message)
	}
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func renderItem(w io.Writer, it any) {
	switch x := it.(type) {
	case messageItem:
		del := ""
		if !x.DeletedAt.IsZero() {
			del = " (deleted)"
		}
		_, _ = fmt.Fprintf(w, "%s  %s  [%s]%s\n    %s\n", stamp(x.SentAt), x.SenderName, x.ConversationDisplayName, del, oneLine(x.Text))
	case conversationItem:
		_, _ = fmt.Fprintf(w, "%s  %-8s %s  (%d members)\n", stamp(x.LastMessageAt), x.Kind, x.DisplayName, x.MemberCount)
	case personItem:
		_, _ = fmt.Fprintf(w, "%s  %s  last seen %s\n", x.DisplayName, x.ID, stamp(x.LastSeenAt))
	case activityItem:
		read := "unread"
		if x.IsRead {
			read = "read"
		}
		_, _ = fmt.Fprintf(w, "%s  %s  %s  %s [%s]\n    %s\n", stamp(x.At), x.Type, read, x.SenderName, x.ConversationDisplayName, oneLine(x.Text))
	case projected:
		parts := make([]string, 0, len(x.keys))
		for _, k := range x.keys {
			var v any
			_ = json.Unmarshal(x.vals[k], &v)
			parts = append(parts, fmt.Sprintf("%s=%s", k, oneLine(fmt.Sprint(v))))
		}
		_, _ = fmt.Fprintln(w, strings.Join(parts, "  "))
	default:
		b, _ := json.Marshal(it)
		_, _ = fmt.Fprintln(w, string(b))
	}
}

// renderText prints a result for a person.
func renderText(w io.Writer, v any) error {
	switch r := v.(type) {
	case *listResult:
		for _, it := range r.Items {
			renderItem(w, it)
		}
		more := ""
		if r.Truncated {
			more = " (more exist; raise --limit)"
		}
		_, _ = fmt.Fprintf(w, "%d items%s\n", r.Count, more)
		metaLines(w, r.meta)
	case syncer.Report:
		_, _ = fmt.Fprintf(w, "sync %s\n", r.Status)
		for _, p := range []struct {
			n string
			c any
		}{{"conversations", r.Conversations}, {"messages", r.Messages}, {"people", r.People}, {"activity", r.Activity}} {
			b, _ := json.Marshal(p.c)
			_, _ = fmt.Fprintf(w, "  %-14s %s\n", p.n, b)
		}
		keys := make([]string, 0, len(r.Omissions))
		for k := range r.Omissions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			_, _ = fmt.Fprintf(w, "  omitted %s: %d\n", k, r.Omissions[k])
		}
	case *statusResult:
		renderStatus(w, r)
		metaLines(w, r.meta)
	case *whoamiResult:
		for _, a := range r.Accounts {
			name := a.DisplayName
			if name == "" {
				name = "(name not seen yet)"
			}
			_, _ = fmt.Fprintf(w, "%s  tenant %s  user %s\n", name, a.TenantID, a.UserID)
		}
		if len(r.Accounts) == 0 {
			_, _ = fmt.Fprintln(w, "no accounts archived yet; run teamscrawl sync")
		}
		renderStatus(w, r.Archive)
		metaLines(w, r.meta)
	case *sqlResult:
		_, _ = fmt.Fprintln(w, strings.Join(r.Columns, "\t"))
		for _, row := range r.Rows {
			cells := make([]string, len(row))
			for i, c := range row {
				cells[i] = fmt.Sprint(c)
			}
			_, _ = fmt.Fprintln(w, strings.Join(cells, "\t"))
		}
		metaLines(w, r.meta)
	case *doctorResult:
		for _, c := range r.Checks {
			mark := "ok  "
			switch {
			case !c.OK:
				mark = "FAIL"
			case c.Warn:
				mark = "warn"
			}
			_, _ = fmt.Fprintf(w, "%s %-18s %s\n", mark, c.Name, c.Detail)
			if c.Fix != "" && (!c.OK || c.Warn) {
				_, _ = fmt.Fprintf(w, "     fix: %s\n", c.Fix)
			}
		}
	default:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(w, string(b))
	}
	return nil
}

func renderStatus(w io.Writer, r *statusResult) {
	if !r.ArchiveExists {
		_, _ = fmt.Fprintf(w, "archive %s does not exist yet; run teamscrawl sync\n", r.ArchivePath)
		return
	}
	_, _ = fmt.Fprintf(w, "archive %s (schema v%d, fts %v)\n", r.ArchivePath, r.SchemaVersion, r.FTSPresent)
	for _, a := range r.Accounts {
		_, _ = fmt.Fprintf(w, "  %s/%s  %d conversations, %d messages, %d people, %d activity, newest %s\n",
			a.TenantID, a.UserID, a.Conversations, a.Messages, a.People, a.Activity, stamp(a.NewestSentAt))
	}
	if r.LastRun != nil {
		_, _ = fmt.Fprintf(w, "last run: %s at %s\n", r.LastRun.Status, stamp(r.LastRun.FinishedAt))
	}
	if len(r.OtherOrigins) > 0 {
		_, _ = fmt.Fprintf(w, "other origins: %s\n", strings.Join(r.OtherOrigins, ", "))
	}
}
