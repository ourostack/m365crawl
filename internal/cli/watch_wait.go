package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

// waitDoc is the wait/1 envelope `watch <conversation> --timeout` prints: what it awaited, what happened, and the
// cursor and exact command that wait again from where it stopped.
type waitDoc struct {
	Ax     string         `json:"ax"`
	For    string         `json:"for"`
	Target *previewTarget `json:"target"`
	State  string         `json:"state"`
	Since  string         `json:"since"`
	Until  string         `json:"until"`
	Cursor string         `json:"cursor"`
	Events []waitEvent    `json:"events"`
	Fresh  waitFresh      `json:"fresh"`
	Resume *previewStep   `json:"resume"`
	Next   []previewStep  `json:"next"`
}

type waitEvent struct {
	Cursor  string         `json:"cursor"`
	Type    string         `json:"type"`
	At      string         `json:"at"`
	By      string         `json:"by"`
	Target  *previewTarget `json:"target"`
	Summary string         `json:"summary"`
}

type waitFresh struct {
	State string `json:"state"`
	AsOf  any    `json:"asOf"`
}

// waitResult is the answer of a wait: the wait/1 document, and the archive's read metadata.
type waitResult struct {
	meta
	waitDoc
}

// waitOn waits until a message arrives in the conversation or the timeout passes. It only reads the archive: it
// never syncs, sends or changes anything.
func (c *watchCmd) waitOn(rt *runtime) error {
	since := time.Now().UTC()
	if c.Cursor != "" {
		parsed, err := time.Parse(time.RFC3339Nano, c.Cursor)
		if err != nil {
			return errs.Usage("--cursor is not a cursor from a wait: " + err.Error())
		}
		since = parsed
	}
	return rt.read("watch", func(st *store.Store) (result, error) {
		deadline := time.Now().Add(c.Timeout)
		res := &waitResult{}
		for {
			events, err := c.newMessages(rt, st, since)
			if err != nil {
				return nil, err
			}
			if len(events) > 0 {
				res.waitDoc = c.waitDocOf(since, "satisfied", events)
				return res, nil
			}
			if st == nil || !time.Now().Before(deadline) || rt.ctx.Err() != nil {
				break
			}
			select {
			case <-rt.ctx.Done():
			case <-time.After(100 * time.Millisecond):
			}
		}
		res.waitDoc = c.waitDocOf(since, "timed-out", nil)
		return res, nil
	})
}

// newMessages returns the messages the conversation got after since, as wait events.
func (c *watchCmd) newMessages(rt *runtime, st *store.Store, since time.Time) ([]waitEvent, error) {
	if st == nil {
		return nil, nil
	}
	rows, _, err := st.Messages(rt.ctx, store.Filter{Conversation: c.Conversation, Since: since, Limit: 50})
	if err != nil {
		return nil, err
	}
	events := make([]waitEvent, 0, len(rows))
	for _, row := range rows {
		at := row.SentAt.UTC().Format(time.RFC3339Nano)
		events = append(events, waitEvent{Cursor: at, Type: "message", At: at, By: row.SenderName,
			Target: &previewTarget{Kind: "chat", ID: c.Conversation}, Summary: fmt.Sprintf("%s wrote in the chat", row.SenderName)})
	}
	return events, nil
}

// waitDocOf builds the document: satisfied with its events, or timed-out with the command that waits again.
func (c *watchCmd) waitDocOf(since time.Time, state string, events []waitEvent) waitDoc {
	now := time.Now().UTC()
	cursor := now.Format(time.RFC3339Nano)
	if n := len(events); n > 0 {
		cursor = events[n-1].Cursor
	}
	doc := waitDoc{
		Ax:     "wait/1",
		For:    "a new message in " + c.Conversation,
		Target: &previewTarget{Kind: "chat", ID: c.Conversation},
		State:  state,
		Since:  since.Format(time.RFC3339Nano),
		Until:  now.Add(c.Timeout).Format(time.RFC3339Nano),
		Cursor: cursor,
		Events: events,
		Fresh:  waitFresh{State: "archive", AsOf: nil},
		Next:   []previewStep{},
	}
	if events == nil {
		doc.Events = []waitEvent{}
	}
	if state == "timed-out" {
		doc.Resume = &previewStep{Command: fmt.Sprintf("m365crawl watch %s --timeout %s --cursor %s", c.Conversation, c.Timeout, cursor),
			Why: "wait again from the cursor for messages that arrive later"}
	}
	return doc
}

// renderWait prints the wait for a person: what came in, or that nothing did and the command that resumes it.
func (r *waitResult) renderWait(rt *runtime) error {
	d := r.waitDoc
	if d.State == "timed-out" {
		_, err := fmt.Fprintf(rt.stdout, "nothing new in %s yet; resume with `%s`\n", d.Target.ID, d.Resume.Command)
		return err
	}
	var lines []string
	for _, e := range d.Events {
		lines = append(lines, e.At+"  "+e.Summary)
	}
	_, err := fmt.Fprintln(rt.stdout, strings.Join(lines, "\n"))
	return err
}
