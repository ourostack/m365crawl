package cli

import (
	"fmt"
	"strconv"

	"github.com/ourostack/m365crawl/internal/render"
)

var (
	scoutSessionColumns = []string{"activity", "id", "device", "origin", "messages", "state", "name"}
	scoutMessageColumns = []string{"at", "session", "role", "session_name", "text"}
	scoutSourceColumns  = []string{"source", "folder", "last_scan", "outcome", "present", "archived", "cloud_only", "failed", "gone"}
)

func scoutOrigin(x scoutSessionItem) string {
	switch {
	case x.Origin != nil:
		return *x.Origin
	case x.Automation:
		return "automation"
	}
	return "interactive"
}

func scoutSessionRow(x scoutSessionItem) []string {
	activity := x.LastMessageAt
	if activity == nil {
		activity = x.UpdatedAt
	}
	if activity == nil {
		activity = &x.FileModifiedAt
	}
	return []string{noteStamp(activity), x.ID, shortDevice(x.DeviceID), scoutOrigin(x), strconv.Itoa(x.MessageCount), x.FileState, oneLine(firstOf(x.Name, "(untitled)"))}
}

func scoutMessageRow(x scoutMessageItem) []string {
	return []string{noteStamp(x.At), x.Session, x.Role, oneLine(firstOf(x.SessionName, "(untitled)")), oneLine(x.Text)}
}

func scoutSourceRow(x scoutSourceItem) []string {
	return []string{strconv.FormatInt(x.SourceID, 10), x.LocatorTail, noteStamp(x.LastScanAt), x.LastOutcome, strconv.Itoa(x.PresentFiles),
		strconv.Itoa(x.ArchivedFiles), strconv.Itoa(x.CloudOnlyFiles), strconv.Itoa(x.FailedFiles), strconv.Itoa(x.GoneFiles)}
}

// shortDevice is the first eight characters of a device folder name, enough to tell devices apart.
func shortDevice(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// renderScoutDetails prints the messages of a scout show item under its one-row table.
func (rt *runtime) renderScoutDetails(r *listResult) {
	for _, it := range r.Items {
		x, ok := it.(scoutDetailItem)
		if !ok {
			continue
		}
		_, _ = fmt.Fprintf(rt.stdout, "\nobservation %d of %d versions; %s; captured %s; digest %s\n", x.ObservationID, x.Observations,
			plural(x.MessageCount, "message", "messages"), stamp(x.CapturedAt), x.Digest)
		rows := [][]string{}
		for _, m := range x.Messages {
			rows = append(rows, []string{strconv.Itoa(m.Ordinal), noteStamp(m.At), m.Role, oneLine(m.Text)})
		}
		rt.renderListRows([]string{"#", "at", "role", "text"}, rows, 3, map[int]string{})
		if x.MessagesTruncated {
			_, _ = fmt.Fprintf(rt.stdout, "%s\n", render.Dim(fmt.Sprintf("more messages follow: use --offset %d", x.MessagesOffset+len(x.Messages)), rt.color))
		}
	}
}

func (rt *runtime) renderScoutCoverage(c *scoutCoverage) {
	if c == nil {
		return
	}
	t := c.Totals
	_, _ = fmt.Fprintf(rt.stdout, "%s\n", render.Dim(fmt.Sprintf("scout: %s, best-effort (%s format); %s archived; files now: %d archived, %d cloud-only (not read), %d failed, %d gone",
		c.State, c.Format, plural(t.Sessions, "session", "sessions"), t.Archived, t.CloudOnly, t.Failed, t.Gone), rt.color))
	for _, src := range c.Sources {
		if src.Fix != "" {
			_, _ = fmt.Fprintf(rt.stdout, "scout source %d (%s): %s\n  fix: %s\n", src.SourceID, src.LocatorTail, firstOf(src.LastReason, src.LastOutcome), src.Fix)
		}
	}
}

func (rt *runtime) renderScoutSync(r scoutSyncResult) {
	_, _ = fmt.Fprintf(rt.stdout, "scout: %d admitted, %d unchanged, %d cloud-only (not read), %d failed, %d gone\n", r.Admitted, r.Unchanged, r.CloudOnly, r.Failed, r.Gone)
	rows := [][]string{}
	for _, f := range r.Folders {
		rows = append(rows, []string{strconv.FormatInt(f.SourceID, 10), f.LocatorTail, f.Outcome, strconv.Itoa(f.Listed), strconv.Itoa(f.Admitted),
			strconv.Itoa(f.Unchanged), strconv.Itoa(f.CloudOnly), strconv.Itoa(f.Failed + f.Unsafe + f.TooLarge), strconv.Itoa(f.Gone)})
	}
	render.Table(rt.stdout, []string{"source", "folder", "outcome", "listed", "admitted", "unchanged", "cloud_only", "failed", "gone"}, rows, rt.color)
	for _, f := range r.Folders {
		if f.Fix != "" {
			_, _ = fmt.Fprintf(rt.stdout, "%s: %s\n  fix: %s\n", f.LocatorTail, firstOf(f.Reason, f.Outcome), f.Fix)
		}
	}
	for _, notice := range r.Notices {
		_, _ = fmt.Fprintf(rt.stdout, "notice: %s\n", notice)
	}
}
