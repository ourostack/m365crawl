package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A scenario mailbox, selected by WORKIQ_MAILBOX, answers every mail path from a JSON file: a
// folder tree, messages, and events that change the mailbox once the shared fetch counter (kept
// in the scenario's state file across every child process) reaches their "at". It reproduces the
// live shapes: delta pages of 10 newest first, parenthesized links WorkIQ refuses until rewritten,
// a filtered delta that honors only "receivedDateTime ge", listings of at most 100 with $skip and
// @odata.count, and count-only probes.
type mailbox struct {
	Folders  []mbFolder  `json:"folders"`
	Messages []mbMessage `json:"messages"`
	Events   []mbEvent   `json:"events"`
	State    string      `json:"state"`
	// Expire answers one delta or skip token of a folder with 410 once the counter reaches At.
	Expire []struct {
		Folder string `json:"folder"`
		At     int    `json:"at"`
	} `json:"expire"`
	// RemovalsBeforeSince makes a filtered delta also report removals of mail received before since.
	RemovalsBeforeSince bool `json:"removals_before_since"`
	// PauseAt makes the fetch numbered PauseAt wait while PauseFile exists, so a test can interleave
	// two processes at a known point.
	PauseAt   int    `json:"pause_at"`
	PauseFile string `json:"pause_file"`
	// Break makes one kind of answer malformed, for refusal tests: count (no @odata.count), folder
	// (an entity without an id), collection (no value array), child_parent (a child naming another
	// parent), next_scope (a next link outside its folder) or many_folders (more than 100000).
	Break string `json:"break"`
}

type mbFolder struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	WellKnown string `json:"well_known"`
	Parent    string `json:"parent"`
	Hidden    bool   `json:"hidden"`
}

type mbMessage struct {
	ID       string `json:"id"`
	Folder   string `json:"folder"`
	Header   string `json:"header"`
	Subject  string `json:"subject"`
	Sender   string `json:"sender"`
	Sent     string `json:"sent"`
	Received string `json:"received"`
	Modified string `json:"modified"`
	Pad      int    `json:"pad"`
}

type mbEvent struct {
	At int    `json:"at"`
	Op string `json:"op"` // add, remove, move, modify or add_folder (ID, To as parent)
	mbMessage
	To    string `json:"to"`
	NewID string `json:"new_id"`
}

type mbState struct {
	Fetches int            `json:"fetches"`
	Applied int            `json:"applied"`
	Expired map[string]int `json:"expired"`
}

func loadMailbox() *mailbox {
	name := os.Getenv("WORKIQ_MAILBOX")
	if name == "" {
		return nil
	}
	raw, err := os.ReadFile(name) //nolint:gosec // G304: test-chosen scenario path.
	if err != nil {
		os.Exit(92)
	}
	var mb mailbox
	if json.Unmarshal(raw, &mb) != nil {
		os.Exit(93)
	}
	return &mb
}

// tick counts one fetch, applies the events it reaches and returns the state, under a directory
// lock shared by every child process.
func (mb *mailbox) tick() mbState {
	lock := mb.State + ".lock"
	for os.Mkdir(lock, 0o700) != nil {
		time.Sleep(5 * time.Millisecond)
	}
	defer func() { _ = os.Remove(lock) }()
	var st mbState
	if raw, err := os.ReadFile(mb.State); err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	if st.Expired == nil {
		st.Expired = map[string]int{}
	}
	st.Fetches++
	for st.Applied < len(mb.Events) && mb.Events[st.Applied].At <= st.Fetches {
		st.Applied++
	}
	raw, _ := json.Marshal(st)
	if os.WriteFile(mb.State, raw, 0o600) != nil {
		os.Exit(94)
	}
	return st
}

func (mb *mailbox) saveExpired(key string) bool {
	lock := mb.State + ".lock"
	for os.Mkdir(lock, 0o700) != nil {
		time.Sleep(5 * time.Millisecond)
	}
	defer func() { _ = os.Remove(lock) }()
	var st mbState
	raw, _ := os.ReadFile(mb.State)
	_ = json.Unmarshal(raw, &st)
	if st.Expired == nil {
		st.Expired = map[string]int{}
	}
	if st.Expired[key] > 0 {
		return false
	}
	st.Expired[key]++
	raw, _ = json.Marshal(st)
	_ = os.WriteFile(mb.State, raw, 0o600)
	return true
}

// messagesAt is the mailbox after its first applied events.
func (mb *mailbox) messagesAt(applied int) []mbMessage {
	byID := map[string]mbMessage{}
	var order []string
	for _, m := range mb.Messages {
		byID[m.ID] = m
		order = append(order, m.ID)
	}
	for _, e := range mb.Events[:applied] {
		switch e.Op {
		case "add":
			byID[e.ID] = e.mbMessage
			order = append(order, e.ID)
		case "remove":
			delete(byID, e.ID)
		case "move":
			m := byID[e.ID]
			delete(byID, e.ID)
			m.ID, m.Folder = e.NewID, e.To
			byID[m.ID] = m
			order = append(order, m.ID)
		case "modify":
			m := byID[e.ID]
			m.Modified = e.Modified
			byID[e.ID] = m
		}
	}
	var out []mbMessage
	seen := map[string]bool{}
	for _, id := range order {
		if m, ok := byID[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, m)
		}
	}
	return out
}

// foldersAt is the folder tree after the first applied events.
func (mb *mailbox) foldersAt(applied int) []mbFolder {
	out := append([]mbFolder(nil), mb.Folders...)
	for _, e := range mb.Events[:applied] {
		if e.Op == "add_folder" {
			out = append(out, mbFolder{ID: e.ID, Name: e.ID, Parent: e.To})
		}
	}
	return out
}

func (m mbMessage) json() map[string]any {
	sender := m.Sender
	if sender == "" {
		sender = "ann@example.test"
	}
	subject := m.Subject
	if subject == "" {
		subject = "Synthetic " + m.ID
	}
	sent := m.Sent
	if sent == "" {
		sent = m.Received
	}
	out := map[string]any{"id": m.ID, "parentFolderId": m.Folder, "internetMessageId": m.Header, "subject": subject,
		"sentDateTime": sent, "lastModifiedDateTime": m.Modified, "bodyPreview": "Synthetic preview",
		"from": map[string]any{"emailAddress": map[string]string{"name": "Ann Sender", "address": sender}},
		"body": map[string]string{"contentType": "html", "content": "<p>Synthetic body " + m.ID + strings.Repeat(" x", m.Pad/2) + "</p>"}}
	if m.Received != "" {
		out["receivedDateTime"] = m.Received
	}
	return out
}

func mbTime(s string) time.Time {
	at, _ := time.Parse(time.RFC3339, s)
	return at
}

// window parses the window filter: "receivedDateTime ge A" with an optional "and receivedDateTime lt B".
func window(filter string) (time.Time, time.Time, bool) {
	if filter == "" {
		return time.Time{}, time.Time{}, true
	}
	ge, lt, two := strings.Cut(filter, " and ")
	since, ok := strings.CutPrefix(ge, "receivedDateTime ge ")
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	var until time.Time
	if two {
		end, ok := strings.CutPrefix(lt, "receivedDateTime lt ")
		if !ok {
			return time.Time{}, time.Time{}, false
		}
		until = mbTime(end)
	}
	return mbTime(since), until, true
}

func (m mbMessage) within(since, until time.Time) bool {
	if m.Received == "" {
		return true // an undated item is in every filter's walk, as a client cannot rule it out
	}
	r := mbTime(m.Received)
	return !r.Before(since) && (until.IsZero() || r.Before(until))
}

func (mb *mailbox) fetch(path string) (any, int, bool) {
	u, err := url.ParseRequestURI(path)
	if err != nil || !strings.HasPrefix(u.Path, "/me/mailFolders") {
		return nil, 0, false
	}
	st := mb.tick()
	if mb.PauseAt > 0 && st.Fetches == mb.PauseAt {
		for {
			if _, err := os.Stat(mb.PauseFile); err != nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	q := u.Query()
	rest := strings.TrimPrefix(u.Path, "/me/mailFolders")
	folders := mb.foldersAt(st.Applied)
	if broken, ok := mb.broken(rest, q); ok {
		return broken, 200, true
	}
	if rest == "" {
		var values []any
		for _, f := range folders {
			if f.Parent == "" {
				values = append(values, mb.folderJSON(f, folders))
			}
		}
		return map[string]any{"value": values}, 200, true
	}
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	id := parts[0]
	if len(parts) == 1 {
		for _, f := range folders {
			if f.WellKnown == id || f.ID == id {
				return mb.folderJSON(f, folders), 200, true
			}
		}
		return map[string]any{"id": "absent-" + id, "displayName": id, "childFolderCount": 0, "totalItemCount": 0}, 200, true
	}
	switch strings.Join(parts[1:], "/") {
	case "childFolders":
		values := []any{}
		for _, f := range folders {
			if f.Parent == id {
				values = append(values, mb.folderJSON(f, folders))
			}
		}
		return map[string]any{"value": values}, 200, true
	case "messages":
		return mb.listing(id, q, st)
	case "messages/delta":
		return mb.delta(id, q, st)
	}
	return nil, 404, true
}

func (mb *mailbox) broken(rest string, q url.Values) (any, bool) {
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	switch {
	case mb.Break == "count" && q.Get("$top") == "1":
		return map[string]any{"value": []any{}}, true
	case mb.Break == "folder" && len(parts) == 1 && rest != "":
		return map[string]any{"displayName": "no id"}, true
	case mb.Break == "collection" && rest == "":
		return map[string]any{"value": "not-a-collection"}, true
	case mb.Break == "dup_child" && strings.HasSuffix(rest, "/childFolders"):
		return map[string]any{"value": []any{map[string]any{"id": "inbox-id", "childFolderCount": 0}}}, true
	case mb.Break == "bad_child" && strings.HasSuffix(rest, "/childFolders"):
		return map[string]any{"value": []any{map[string]any{"displayName": "no id"}}}, true
	case mb.Break == "root_next_scope" && rest == "":
		return map[string]any{"value": []any{}, "@odata.nextLink": "/me/messages?$skip=1"}, true
	case mb.Break == "child_parent" && strings.HasSuffix(rest, "/childFolders"):
		return map[string]any{"value": []any{map[string]any{"id": "stray", "parentFolderId": "elsewhere", "childFolderCount": 0}}}, true
	case mb.Break == "next_scope" && strings.HasSuffix(rest, "/messages/delta") && q.Get("$skiptoken") == "" && q.Get("$deltatoken") == "":
		return map[string]any{"value": []any{}, "@odata.nextLink": "/me/mailFolders/elsewhere/messages/delta?$skiptoken=x"}, true
	case mb.Break == "many_folders" && rest == "":
		page := q.Get("$skiptoken")
		values := make([]any, 0, 60000)
		for i := 0; i < 60000; i++ {
			values = append(values, map[string]any{"id": fmt.Sprintf("f-%s-%d", page, i)})
		}
		out := map[string]any{"value": values}
		if page == "" {
			out["@odata.nextLink"] = "/me/mailFolders?includeHiddenFolders=true&$skiptoken=2"
		}
		return out, true
	}
	return nil, false
}

func (mb *mailbox) folderJSON(f mbFolder, folders []mbFolder) map[string]any {
	children, total := 0, 0
	for _, c := range folders {
		if c.Parent == f.ID {
			children++
		}
	}
	for _, m := range mb.messagesAt(len(mb.Events)) {
		if m.Folder == f.ID {
			total++
		}
	}
	parent := f.Parent
	if parent == "" {
		parent = "msgfolderroot"
	}
	return map[string]any{"id": f.ID, "displayName": f.Name, "parentFolderId": parent, "childFolderCount": children, "totalItemCount": total, "isHidden": f.Hidden}
}

func (mb *mailbox) listing(folder string, q url.Values, st mbState) (any, int, bool) {
	since, until, ok := window(q.Get("$filter"))
	if !ok {
		return nil, 400, true
	}
	var items []mbMessage
	for _, m := range mb.messagesAt(st.Applied) {
		if m.Folder == folder && m.Received != "" && m.within(since, until) {
			items = append(items, m)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Received != items[j].Received {
			return items[i].Received < items[j].Received
		}
		return items[i].Header < items[j].Header
	})
	top, _ := strconv.Atoi(q.Get("$top"))
	if top < 1 || top > 100 {
		top = 100
	}
	skip, _ := strconv.Atoi(q.Get("$skip"))
	out := map[string]any{"@odata.count": len(items)}
	values := []any{}
	for i := skip; i < len(items) && i < skip+top; i++ {
		values = append(values, items[i].json())
	}
	out["value"] = values
	if skip+top < len(items) {
		next := url.Values{}
		for key, v := range q {
			next[key] = v
		}
		next.Set("$skip", strconv.Itoa(skip+top))
		out["@odata.nextLink"] = "/me/mailFolders('" + folder + "')/messages?" + next.Encode()
	}
	return out, 200, true
}

// delta serves a baseline walk ($skiptoken b.<seq>.<offset>.<since>) of the mailbox as it was at
// its first page, and a follow ($deltatoken <seq>.<since>) of every change since that sequence.
func (mb *mailbox) delta(folder string, q url.Values, st mbState) (any, int, bool) {
	link := "/me/mailFolders('" + folder + "')/messages/delta"
	var since time.Time
	base, offset, from := st.Applied, 0, -1
	if filter := q.Get("$filter"); filter != "" {
		start, until, ok := window(filter)
		if !ok || !until.IsZero() {
			return nil, 400, true
		}
		since = start
	}
	token := q.Get("$skiptoken")
	if token == "" {
		token = q.Get("$deltatoken")
		if token != "" {
			token = "f." + token
		}
	}
	if token != "" {
		for _, e := range mb.Expire {
			if e.Folder == folder && st.Fetches >= e.At && mb.saveExpired(fmt.Sprintf("%s@%d", folder, e.At)) {
				return map[string]any{"error": map[string]any{"code": "SyncStateNotFound", "message": "expired"}}, 410, true
			}
		}
		fields := strings.Split(token, ".")
		switch fields[0] {
		case "b": // b.<seq>.<offset>.<since>
			base, _ = strconv.Atoi(fields[1])
			offset, _ = strconv.Atoi(fields[2])
			since = time.Unix(mustInt(fields[3]), 0).UTC()
		case "f": // f.<from>.<since>: the first page of a follow
			from, _ = strconv.Atoi(fields[1])
			since = time.Unix(mustInt(fields[2]), 0).UTC()
		case "d": // d.<from>.<to>.<offset>.<since>
			from, _ = strconv.Atoi(fields[1])
			base, _ = strconv.Atoi(fields[2])
			offset, _ = strconv.Atoi(fields[3])
			since = time.Unix(mustInt(fields[4]), 0).UTC()
		}
	}
	inFolder := func(applied int) map[string]mbMessage {
		out := map[string]mbMessage{}
		for _, m := range mb.messagesAt(applied) {
			if m.Folder == folder {
				out[m.ID] = m
			}
		}
		return out
	}
	now := inFolder(base)
	var values []any
	if from < 0 {
		var items []mbMessage
		for _, m := range now {
			if m.within(since, time.Time{}) {
				items = append(items, m)
			}
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].Received != items[j].Received {
				return items[i].Received > items[j].Received
			}
			return items[i].ID < items[j].ID
		})
		for _, m := range items {
			values = append(values, m.json())
		}
	} else {
		before := inFolder(from)
		var ids []string
		for id := range now {
			ids = append(ids, id)
		}
		for id := range before {
			if _, ok := now[id]; !ok {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			m, live := now[id]
			old, had := before[id]
			switch {
			case live && m.within(since, time.Time{}) && (!had || old.Modified != m.Modified):
				values = append(values, m.json())
			case !live && (old.within(since, time.Time{}) || mb.RemovalsBeforeSince):
				values = append(values, map[string]any{"id": id, "@removed": map[string]any{"reason": "deleted"}})
			}
		}
	}
	end := min(offset+10, len(values))
	page := values[offset:end]
	if page == nil {
		page = []any{}
	}
	out := map[string]any{"value": page}
	unix := strconv.FormatInt(since.Unix(), 10)
	switch {
	case end < len(values) && from < 0:
		out["@odata.nextLink"] = fmt.Sprintf("%s?$skiptoken=b.%d.%d.%s", link, base, end, unix)
	case end < len(values):
		out["@odata.nextLink"] = fmt.Sprintf("%s?$skiptoken=d.%d.%d.%d.%s", link, from, base, end, unix)
	default:
		out["@odata.deltaLink"] = fmt.Sprintf("%s?$deltatoken=%d.%s", link, base, unix)
	}
	return out, 200, true
}

func mustInt(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
