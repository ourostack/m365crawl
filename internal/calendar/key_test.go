package calendar

import (
	"strings"
	"testing"
	"time"
)

func TestKeyMovedOccurrenceStable(t *testing.T) {
	orig := tp(t, "2026-10-05T16:00:00Z")
	a := Event{GlobalID: "uid-1", OriginalStart: orig, Start: mustTime(t, "2026-10-05T16:00:00Z")}
	b := Event{GlobalID: "uid-1", OriginalStart: orig, Start: mustTime(t, "2026-10-06T18:00:00Z")}
	if Key(a) != Key(b) {
		t.Fatalf("moved occurrence changed key: %q vs %q", Key(a), Key(b))
	}
	if got, want := Key(a), "uid-1|2026-10-05T16:00:00Z"; got != want {
		t.Fatalf("Key = %q, want %q", got, want)
	}
	other := Event{GlobalID: "uid-1", OriginalStart: tp(t, "2026-10-12T16:00:00Z")}
	if Key(a) == Key(other) {
		t.Fatal("different occurrences share a key")
	}
	// An original start in another zone names the same instant.
	zoned := mustTime(t, "2026-10-05T09:00:00-07:00")
	if got := Key(Event{GlobalID: "uid-1", OriginalStart: &zoned}); got != Key(a) {
		t.Fatalf("zoned original = %q, want %q", got, Key(a))
	}
}

func TestKeySingleEvent(t *testing.T) {
	e := Event{GlobalID: "uid-2", Start: mustTime(t, "2026-10-05T16:00:00Z")}
	if got, want := Key(e), "uid-2|"; got != want {
		t.Fatalf("Key = %q, want %q", got, want)
	}
}

func TestKeyAllDayRecurringUsesDate(t *testing.T) {
	// The same all-day occurrence handed over as UTC midnight or as local midnight keeps its date.
	utc := Event{GlobalID: "uid-3", AllDay: true, OriginalStart: tp(t, "2026-10-05T00:00:00Z")}
	local := Event{GlobalID: "uid-3", AllDay: true, OriginalStart: tp(t, "2026-10-05T00:00:00+09:00")}
	for _, e := range []Event{utc, local} {
		if got, want := Key(e), "uid-3|2026-10-05"; got != want {
			t.Fatalf("Key = %q, want %q", got, want)
		}
	}
	single := Event{GlobalID: "uid-3", AllDay: true, StartDate: "2026-10-05"}
	if got, want := Key(single), "uid-3|"; got != want {
		t.Fatalf("single all-day Key = %q, want %q", got, want)
	}
}

func TestKeyComposite(t *testing.T) {
	base := Event{
		Organizer: "  Ada@Example.com ", Subject: "  Weekly   Sync\tmeeting ",
		Start: mustTime(t, "2026-10-05T16:00:00Z"),
	}
	key := Key(base)
	if !strings.HasPrefix(key, "composite|") || len(key) != len("composite|")+24 {
		t.Fatalf("composite key shape: %q", key)
	}
	// Order independent and normalized: the other tool mints the same key from the same data.
	twin := Event{Source: SourceOutlook, Organizer: "ada@example.com", Subject: "weekly sync meeting", Start: mustTime(t, "2026-10-05T09:00:00-07:00")}
	if Key(twin) != key {
		t.Fatalf("twin key %q != %q", Key(twin), key)
	}
	if Key(Event{Organizer: "ada@example.com", Subject: "weekly sync", Start: base.Start}) == key {
		t.Fatal("different subject must change the key")
	}
	// The original start wins over the moved start.
	moved := base
	moved.OriginalStart = tp(t, "2026-10-05T16:00:00Z")
	moved.Start = mustTime(t, "2026-10-06T10:00:00Z")
	if Key(moved) != key {
		t.Fatalf("moved composite key %q != %q", Key(moved), key)
	}
	// All-day events use dates: the original date, else the start date.
	d1 := Event{Organizer: "a", Subject: "off", AllDay: true, StartDate: "2026-10-05"}
	d2 := Event{Organizer: "a", Subject: "off", AllDay: true, StartDate: "2026-10-09", OriginalStart: tp(t, "2026-10-05T00:00:00Z")}
	if Key(d1) != Key(d2) {
		t.Fatalf("all-day composite keys differ: %q %q", Key(d1), Key(d2))
	}
	if Key(d1) == Key(Event{Organizer: "a", Subject: "off", AllDay: true, StartDate: "2026-10-06"}) {
		t.Fatal("different all-day date must change the key")
	}
	if compositeKey(base) != key {
		t.Fatal("compositeKey must equal Key when there is no global id")
	}
	g := base
	g.GlobalID = "uid"
	if compositeKey(g) != key {
		t.Fatal("compositeKey ignores the global id")
	}
}

func TestParseTeamsThreadID(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "https://teams.microsoft.com/l/meetup-join/19:meeting_AbC-12_x@thread.v2/0?context=%7b%7d", "19:meeting_AbC-12_x@thread.v2"},
		{"none", "https://example.com/join", ""},
		{"empty", "", ""},
		{"not a meeting", "https://teams.microsoft.com/l/chat/19:abc@thread.v2/0", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseTeamsThreadID(tt.in); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseTeamsThreadIDEncoded(t *testing.T) {
	enc := "https://teams.microsoft.com/l/meetup-join/19%3ameeting_AbC-12_x%40thread.v2/0?context=%7b%22Tid%22%3a%22t%22%7d"
	if got, want := ParseTeamsThreadID(enc), "19:meeting_AbC-12_x@thread.v2"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// A stray percent sign elsewhere does not hide an id that is already plain.
	bad := "https://teams.microsoft.com/l/meetup-join/19:meeting_Z@thread.v2/0?x=100%"
	if got, want := ParseTeamsThreadID(bad), "19:meeting_Z@thread.v2"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

var _ = time.Second
