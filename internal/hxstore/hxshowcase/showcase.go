// Package hxshowcase builds a synthetic Outlook for Mac store that looks like a working mailbox:
// a handful of folders, a few dozen messages (some unread, some with attachments, some sent) and a
// calendar of meetings around the present. `make screenshot` syncs it, with the Teams fixture,
// to render the README image.
//
// Everything in it is invented. The people and the company are fictional and every address ends
// in example.test. Nothing here reads a real store, a clock or a random source: every time is an
// offset from the now the caller passes, so the same now gives the same bytes, and a store built
// for the present never reads as stale.
package hxshowcase

import (
	"fmt"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

// Profile is the name of the profile directory the store belongs in.
const Profile = "Main"

// Owner is the mailbox's own address.
const Owner = "riley.chen@example.test"

// Folder types of the store (docs/outlook-store.md, "Mail").
const (
	typeInbox   = 0x61
	typeArchive = 0x63
	typeDrafts  = 0x64
	typeSent    = 0x65
	typeDeleted = 0x67
	typeUser    = 0x7a // the generic type user-created folders have
)

// rootKey is the account root the folders hang from.
const rootKey = 1000

// Folder is one folder of the mailbox.
type Folder struct {
	Key  uint32
	Name string
	Type uint32
}

// Folders lists the mailbox's folders.
var Folders = []Folder{
	{101, "Inbox", typeInbox},
	{102, "Sent Items", typeSent},
	{103, "Drafts", typeDrafts},
	{104, "Archive", typeArchive},
	{105, "Deleted Items", typeDeleted},
	{106, "Launch", typeUser},
	{107, "Customers", typeUser},
}

type person struct{ name, addr string }

var (
	riley  = person{"Riley Chen", Owner}
	sam    = person{"Sam Ortiz", "sam.ortiz@example.test"}
	priya  = person{"Priya Nair", "priya.nair@example.test"}
	jordan = person{"Jordan Lee", "jordan.lee@example.test"}
	morgan = person{"Morgan Diaz", "morgan.diaz@example.test"}
	alex   = person{"Alex Kim", "alex.kim@example.test"}
	casey  = person{"Casey Brooks", "casey.brooks@example.test"}
	builds = person{"Build Service", "builds@example.test"}
	desk   = person{"Help Desk", "helpdesk@example.test"}
)

// Message is one message as the builder writes it.
type Message struct {
	Folder     uint32
	Ago        time.Duration // received this long before now
	From       person
	To         []person
	Subject    string
	Preview    string
	Unread     bool
	Flagged    bool
	High       bool
	Attachment string // a file name; empty for none
}

// Messages lists the mailbox's messages, newest first.
func Messages() []Message {
	h := time.Hour
	d := 24 * h
	me := []person{riley}
	return []Message{
		{Folder: 101, Ago: 12 * time.Minute, From: sam, To: me, Subject: "Launch checklist: final review", Preview: "Everything is green except the docs pass. Can you take a last look before 3?", Unread: true, High: true, Attachment: "launch-checklist.pdf"},
		{Folder: 101, Ago: 47 * time.Minute, From: priya, To: me, Subject: "Re: Pricing page copy", Preview: "Went with the shorter headline. Screenshots attached for sign-off.", Unread: true, Attachment: "pricing-v3.png"},
		{Folder: 101, Ago: 2 * h, From: builds, To: me, Subject: "Build 1482 passed on main", Preview: "All 214 checks passed in 6m 12s.", Unread: true},
		{Folder: 101, Ago: 3 * h, From: jordan, To: me, Subject: "Customer interviews: notes from Tuesday", Preview: "Three themes kept coming up: search, offline access and export.", Unread: true, Flagged: true},
		{Folder: 101, Ago: 5 * h, From: morgan, To: me, Subject: "Press briefing moved to Thursday", Preview: "The analyst call slid a day; the new invite is on your calendar.", Unread: true},
		{Folder: 101, Ago: 7 * h, From: alex, To: me, Subject: "Design review follow-ups", Preview: "Updated the empty states and the dark theme contrast.", Attachment: "review-notes.pdf"},
		{Folder: 101, Ago: 1*d + 2*h, From: casey, To: me, Subject: "Q4 planning offsite agenda", Preview: "Draft agenda inside. Add anything you want covered by Friday."},
		{Folder: 101, Ago: 1*d + 4*h, From: sam, To: me, Subject: "Re: Release notes draft", Preview: "Reordered the highlights so mail and calendar lead."},
		{Folder: 101, Ago: 1*d + 6*h, From: desk, To: me, Subject: "Your laptop refresh is ready", Preview: "Pick it up at the second floor desk any time this week.", Unread: true},
		{Folder: 101, Ago: 1*d + 9*h, From: priya, To: me, Subject: "Onboarding flow metrics", Preview: "Completion is up 9 points since the new first-run screen.", Attachment: "onboarding-metrics.xlsx"},
		{Folder: 101, Ago: 2*d + 1*h, From: jordan, To: me, Subject: "Re: Search relevance tuning", Preview: "The new ranking puts exact phrase matches first. Much better.", Flagged: true},
		{Folder: 101, Ago: 2*d + 5*h, From: builds, To: me, Subject: "Build 1477 passed on main", Preview: "All 214 checks passed in 6m 40s."},
		{Folder: 101, Ago: 3*d + 2*h, From: morgan, To: me, Subject: "Launch blog post: second draft", Preview: "Tightened the intro and added the quote from the beta group."},
		{Folder: 101, Ago: 3*d + 8*h, From: alex, To: me, Subject: "Icon set for the app store listing", Preview: "Final exports at 1x, 2x and 3x.", Attachment: "icons.zip"},
		{Folder: 101, Ago: 4*d + 3*h, From: casey, To: me, Subject: "Team lunch on Friday", Preview: "Booked the usual place for 12:30."},
		{Folder: 101, Ago: 5*d + 1*h, From: sam, To: me, Subject: "Beta feedback roundup", Preview: "Forty-one responses; the top request is calendar export."},
		{Folder: 101, Ago: 6*d + 4*h, From: priya, To: me, Subject: "Accessibility audit results", Preview: "Two contrast issues left, both in settings."},
		{Folder: 102, Ago: 30 * time.Minute, From: riley, To: []person{sam}, Subject: "Re: Launch checklist: final review", Preview: "Looking now. Docs pass will be done by 2."},
		{Folder: 102, Ago: 4 * h, From: riley, To: []person{priya, alex}, Subject: "Re: Pricing page copy", Preview: "Shorter headline works. Ship it."},
		{Folder: 102, Ago: 1*d + 1*h, From: riley, To: []person{jordan}, Subject: "Interview schedule for next week", Preview: "Five slots on Tuesday and Wednesday afternoons."},
		{Folder: 102, Ago: 2*d + 3*h, From: riley, To: []person{morgan}, Subject: "Re: Launch blog post: second draft", Preview: "Great draft. Two small edits inline."},
		{Folder: 102, Ago: 3*d + 6*h, From: riley, To: []person{sam, priya, jordan}, Subject: "Launch week plan", Preview: "Owners and dates for every launch task.", Attachment: "launch-week.pdf"},
		{Folder: 102, Ago: 5*d + 2*h, From: riley, To: []person{casey}, Subject: "Re: Q4 planning offsite agenda", Preview: "Added a session on the support backlog."},
		{Folder: 103, Ago: 1 * h, From: riley, To: []person{morgan}, Subject: "Launch day talking points", Preview: "Draft: what it does, who it is for, what is next."},
		{Folder: 103, Ago: 2*d + 2*h, From: riley, To: []person{alex}, Subject: "Feedback on onboarding illustrations", Preview: "Love the second set. A few notes."},
		{Folder: 104, Ago: 8 * d, From: jordan, To: me, Subject: "Roadmap review notes", Preview: "Agreed priorities for the next two quarters."},
		{Folder: 104, Ago: 9*d + 3*h, From: sam, To: me, Subject: "Security review sign-off", Preview: "No blockers. Two follow-ups filed."},
		{Folder: 104, Ago: 11 * d, From: priya, To: me, Subject: "Analytics dashboard access", Preview: "You now have editor access to the product dashboard."},
		{Folder: 104, Ago: 13*d + 5*h, From: casey, To: me, Subject: "Offsite venue confirmed", Preview: "Booked for the 14th; details inside."},
		{Folder: 105, Ago: 6 * d, From: desk, To: me, Subject: "Scheduled maintenance this weekend", Preview: "Sign-in may be slow on Saturday morning."},
		{Folder: 106, Ago: 6 * h, From: morgan, To: me, Subject: "Launch timeline v4", Preview: "Moved the press embargo to 9 a.m. Pacific.", Attachment: "timeline-v4.pdf"},
		{Folder: 106, Ago: 1*d + 7*h, From: alex, To: me, Subject: "App store screenshots", Preview: "Six screens, light and dark."},
		{Folder: 106, Ago: 3*d + 1*h, From: sam, To: me, Subject: "Launch go/no-go criteria", Preview: "Proposed criteria for the Thursday call."},
		{Folder: 106, Ago: 4*d + 6*h, From: jordan, To: me, Subject: "Support macros for launch", Preview: "Twelve macros ready for the most likely questions."},
		{Folder: 107, Ago: 9 * h, From: casey, To: me, Subject: "Northwind pilot kickoff", Preview: "Kickoff is booked; they want a search demo.", Unread: true},
		{Folder: 107, Ago: 2*d + 7*h, From: jordan, To: me, Subject: "Fabrikam renewal questions", Preview: "They asked about data residency and retention."},
		{Folder: 107, Ago: 5*d + 5*h, From: casey, To: me, Subject: "Contoso feedback call recap", Preview: "They love the agenda view and want calendar export."},
	}
}

// Event is one meeting as the builder writes it.
type Event struct {
	Day      int // days from today (UTC); negative is in the past
	Hour     int // UTC
	Minutes  int // length
	Subject  string
	Location string
	Online   bool
	AllDay   bool
	With     []person
}

// Events lists the calendar, earliest first: two days of the past and the next two weeks.
func Events() []Event {
	return []Event{
		{Day: -2, Hour: 16, Minutes: 30, Subject: "Launch standup", Online: true, With: []person{sam, priya, jordan}},
		{Day: -1, Hour: 17, Minutes: 60, Subject: "Design review", Location: "Room 4B", With: []person{alex, priya}},
		{Day: 0, Hour: 16, Minutes: 30, Subject: "Launch standup", Online: true, With: []person{sam, priya, jordan}},
		{Day: 0, Hour: 21, Minutes: 45, Subject: "Docs pass", Online: true, With: []person{sam}},
		{Day: 1, Hour: 16, Minutes: 30, Subject: "Launch standup", Online: true, With: []person{sam, priya, jordan}},
		{Day: 1, Hour: 18, Minutes: 60, Subject: "Northwind pilot kickoff", Online: true, With: []person{casey, jordan}},
		{Day: 2, Hour: 17, Minutes: 30, Subject: "Launch go/no-go", Location: "Room 2A", Online: true, With: []person{sam, priya, jordan, morgan, alex}},
		{Day: 2, Hour: 20, Minutes: 60, Subject: "Press briefing", Online: true, With: []person{morgan}},
		{Day: 3, Hour: 0, AllDay: true, Subject: "Launch day"},
		{Day: 3, Hour: 16, Minutes: 30, Subject: "Launch standup", Online: true, With: []person{sam, priya, jordan}},
		{Day: 4, Hour: 19, Minutes: 90, Subject: "Launch retro", Location: "Room 4B", With: []person{sam, priya, jordan, morgan, alex, casey}},
		{Day: 7, Hour: 17, Minutes: 60, Subject: "Customer interviews", Online: true, With: []person{jordan}},
		{Day: 8, Hour: 18, Minutes: 30, Subject: "1:1 with Sam", With: []person{sam}},
		{Day: 9, Hour: 16, Minutes: 60, Subject: "Roadmap review", Location: "Room 2A", With: []person{jordan, casey}},
		{Day: 11, Hour: 0, AllDay: true, Subject: "Q4 planning offsite", Location: "Harbor Hall"},
		{Day: 14, Hour: 17, Minutes: 60, Subject: "Fabrikam renewal call", Online: true, With: []person{casey}},
	}
}

// Build returns the store's bytes for the given now. Times are kept to the minute.
func Build(now time.Time) []byte {
	now = now.UTC().Truncate(time.Minute)
	b := hxbuild.New(hxbuild.Options{})
	b.BlockCodec(hxbuild.FramedPayload(hxbuild.Head(15), hxbuild.NewAccount(hxbuild.AccountSpec{Address: Owner})), hxbuild.CodecLiteral)
	b.BlockCodec(hxbuild.FramedPayload(hxbuild.Head(15), folders()...), hxbuild.CodecLiteral)
	b.BlockCodec(hxbuild.FramedPayload(hxbuild.Head(15), mail(now)...), hxbuild.CodecLiteral)
	b.BlockCodec(hxbuild.FramedPayload(hxbuild.Head(15), calendar(now)...), hxbuild.CodecLiteral)
	return b.Bytes()
}

func folders() []*hxbuild.Object {
	out := make([]*hxbuild.Object, 0, len(Folders))
	for _, f := range Folders {
		out = append(out, hxbuild.NewMailFolder(hxbuild.MailFolderSpec{Key: f.Key, Stamp: 1, Parent: rootKey, Name: f.Name, Type: f.Type}))
	}
	return out
}

func mail(now time.Time) []*hxbuild.Object {
	var out []*hxbuild.Object
	for i, m := range Messages() {
		n := uint32(i + 1) //nolint:gosec // a small positive index
		header, detail := 2000+n, 3000+n
		at := now.Add(-m.Ago)
		h := hxbuild.MailHeaderSpec{
			Key: header, Stamp: 1, DetailKey: detail, FolderKey: m.Folder, Received: at,
			Subject: m.Subject, SenderName: m.From.name, SenderAddr: m.From.addr, Preview: m.Preview, Importance: 1,
		}
		if m.Unread {
			h.Unread = 1
		}
		if m.Flagged {
			h.FlagByte = 2
		}
		if m.High {
			h.Importance = 2
		}
		out = append(out,
			hxbuild.NewMailHeader(h),
			hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: detail, Stamp: 1, MessageID: fmt.Sprintf("<showcase-%03d@example.test>", n), Class: "IPM.Note", Sent: at.Add(-time.Minute), Received: at}),
			hxbuild.NewMailBody(hxbuild.MailBodySpec{Key: detail, Stamp: 1, HTML: []byte("<html><body><p>" + m.Preview + "</p><p>" + m.From.name + "</p></body></html>")}),
		)
		for j, to := range m.To {
			out = append(out, hxbuild.NewRecipient(hxbuild.RecipientSpec{Key: 4000 + n*10 + uint32(j), Stamp: 1, Parent: detail, Name: to.name, Address: to.addr, Kind: 1})) //nolint:gosec // a small index
		}
		if m.Attachment != "" {
			out = append(out, hxbuild.NewAttachment(hxbuild.AttachmentSpec{Key: 5000 + n, Stamp: 1, MessageKey: detail, Name: m.Attachment, Size: 48_000 + 1_000*n, State: 2}))
		}
	}
	return out
}

func calendar(now time.Time) []*hxbuild.Object {
	today := now.Truncate(24 * time.Hour)
	var events, details []*hxbuild.Object
	for i, e := range Events() {
		n := i + 1
		start := today.AddDate(0, 0, e.Day).Add(time.Duration(e.Hour) * time.Hour)
		end := start.Add(time.Duration(e.Minutes) * time.Minute)
		if e.AllDay {
			end = start.AddDate(0, 0, 1)
		}
		s := hxbuild.EventSpec{
			ID:           hxbuild.GlobalObjectID(0, 0, 0, fmt.Sprintf("SHOWCASE-EVT-%04d", n)),
			SeriesKey:    0x5c00_0000_0000_0000 | uint64(n), //nolint:gosec // a small positive counter
			DetailKey:    uint32(6000 + n),                  //nolint:gosec // a small positive counter
			LastModified: now.Add(-time.Duration(n) * time.Hour),
			Start:        start, End: end,
			ZoneID: 2, ZoneName: "Pacific Standard Time",
			ShowAs: 2, Response: 1,
			AllDay:  e.AllDay,
			Online:  e.Online,
			Subject: e.Subject, SubjectBare: e.Subject,
			Location:      e.Location,
			OrganizerName: riley.name, OrganizerAddr: riley.addr,
			Preview: e.Subject + ".",
		}
		if e.AllDay {
			s.ShowAs = 0
		}
		for _, p := range e.With {
			s.Attendees = append(s.Attendees, hxbuild.Attendee{Name: p.name, Address: p.addr, B: 1})
		}
		d := hxbuild.DetailSpec{Key: s.DetailKey, SeriesKey: s.SeriesKey, BodyHTML: "<html><body><p>" + e.Subject + "</p></body></html>"}
		if e.Online {
			d.JoinLink = fmt.Sprintf("https://meet.example.test/j/%04d", n)
		}
		events = append(events, hxbuild.NewEvent(s))
		details = append(details, hxbuild.NewDetail(d))
	}
	return append(events, details...)
}
