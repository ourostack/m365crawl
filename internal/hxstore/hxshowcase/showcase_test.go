package hxshowcase

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookmail"
)

var now = time.Date(2026, 10, 7, 18, 4, 30, 0, time.UTC)

func open(t *testing.T, data []byte) *hxstore.Store {
	t.Helper()
	s, err := hxstore.OpenStore(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBuildIsDeterministicAndFollowsNow(t *testing.T) {
	a, b := Build(now), Build(now.Add(20*time.Second))
	if !bytes.Equal(a, b) {
		t.Fatal("the same minute gives different bytes")
	}
	if bytes.Equal(a, Build(now.Add(24*time.Hour))) {
		t.Fatal("another day gives the same bytes")
	}
}

func TestMailReadsBack(t *testing.T) {
	r, err := outlookmail.Collect(context.Background(), open(t, Build(now)), t.TempDir(), "outlook/"+Profile, outlookmail.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != len(Messages()) || len(r.Folders) != len(Folders) || len(r.Losses) != 0 {
		t.Fatalf("messages %d folders %d losses %v", len(r.Messages), len(r.Folders), r.Losses)
	}
	var unread, files, flagged, high int
	for _, m := range r.Messages {
		if m.Unread != nil && *m.Unread {
			unread++
		}
		files += len(m.Attachments)
		if m.Flag == "flagged" {
			flagged++
		}
		if m.Importance == "high" {
			high++
		}
		if age := now.Sub(m.Received); age < 0 || age > 14*24*time.Hour {
			t.Errorf("%q received %v ago", m.Subject, age)
		}
		if !strings.HasSuffix(m.SenderAddress, "@example.test") || m.Folder.Name == "" {
			t.Errorf("%q: sender %q folder %q", m.Subject, m.SenderAddress, m.Folder.Name)
		}
		for _, to := range m.Recipients {
			if !strings.HasSuffix(to.Address, "@example.test") {
				t.Errorf("%q: recipient %q", m.Subject, to.Address)
			}
		}
	}
	if unread != 7 || files != 7 || flagged != 2 || high != 1 {
		t.Fatalf("unread %d attachments %d flagged %d high %d", unread, files, flagged, high)
	}
}

func TestCalendarReadsBack(t *testing.T) {
	r, err := outlookcal.Collect(context.Background(), open(t, Build(now)), "outlook/"+Profile, outlookcal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != len(Events()) || len(r.Losses) != 0 || len(r.UnknownLayouts) != 0 {
		t.Fatalf("events %d losses %v layouts %v", len(r.Events), r.Losses, r.UnknownLayouts)
	}
	if len(r.AccountAddresses) != 1 || r.AccountAddresses[0] != Owner {
		t.Fatalf("accounts %v", r.AccountAddresses)
	}
	upcoming := 0
	for _, e := range r.Events {
		if e.Start.Before(now.AddDate(0, 0, -3)) || e.Start.After(now.AddDate(0, 0, 15)) {
			t.Errorf("%q starts %v", e.Subject, e.Start)
		} else if e.Start.After(now) {
			upcoming++
		}
		if e.TimeZoneIANA == "" {
			t.Errorf("%q: zone %q not resolved", e.Subject, e.TimeZone)
		}
	}
	if upcoming < 10 {
		t.Fatalf("%d upcoming events", upcoming)
	}
}
