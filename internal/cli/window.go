package cli

import (
	"fmt"
	"time"
)

// window parses a lower and an upper time bound (--since/--until, or a command's own pair such as
// --modified-since/--modified-until) into the half-open window [since, until) every date-bounded
// read filters on. A date alone as the upper bound includes that whole local day, so it ends at
// the next local midnight; any other upper bound is exclusive. A zero time is an absent bound. A
// window that ends at or before it starts can match nothing, and the result says so in notices.
func (rt *runtime) window(sinceFlag, sinceRaw, untilFlag, untilRaw string) (since, until time.Time, err error) {
	if since, err = rt.when(sinceFlag, sinceRaw); err != nil {
		return since, until, err
	}
	if until, err = rt.when(untilFlag, untilRaw); err != nil {
		return since, until, err
	}
	if dateOnly.MatchString(untilRaw) {
		until = until.In(time.Local).AddDate(0, 0, 1).UTC()
	}
	if !since.IsZero() && !until.IsZero() && !until.After(since) {
		rt.windowNotice = fmt.Sprintf("the time window is empty, so nothing can match: %s %s ends it at or before %s %s starts it (a date alone as %s includes that whole day; a time is exclusive)",
			untilFlag, untilRaw, sinceFlag, sinceRaw, untilFlag)
	}
	return since, until, nil
}

// noteWindow adds the empty-window notice of this run's window to a result that carries notices.
func (rt *runtime) noteWindow(v any) {
	if n, ok := v.(interface{ addNotice(string) }); ok && rt.windowNotice != "" {
		n.addNotice(rt.windowNotice)
	}
}
