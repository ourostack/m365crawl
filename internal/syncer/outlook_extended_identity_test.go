package syncer

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func putExtendedIdentityStore(t *testing.T, root string, unknown bool) {
	t.Helper()
	start := time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC)
	event := hxbuild.NewEvent(hxbuild.EventSpec{
		ID:           hxbuild.GlobalObjectID(0, 0, 0, "FIXTURE-EXTENDED"),
		LastModified: start, Start: start, End: start.Add(time.Hour), Subject: "Fixture event",
	})
	old := event.Encode()[4:]
	raw := append([]byte(nil), old[:708]...)
	raw = append(raw, 1, 2, 3, 4)
	raw = append(raw, old[708:]...)
	binary.LittleEndian.PutUint16(raw[2:], 0x459)
	o := hxbuild.NewObject(0x6b, 0x459, len(raw))
	for i, b := range raw {
		if i < 4 || i >= 8 {
			o.PutU8(i, b)
		}
	}
	account := hxbuild.NewAccount(hxbuild.AccountSpec{})
	if unknown {
		account = hxbuild.NewObject(0x49, 0x1a12, 6674)
	}
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.FramedPayload(hxbuild.Head(15), o, account))
	if err := os.WriteFile(filepath.Join(root, "Main", "HxStore.hxd"), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExtendedLayoutPreservesUnknownIdentity(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		prior, unknown         bool
		wantIdentity, wantLink string
	}{
		{"existing", true, true, "fixture.owner@example.com", "outlook/Main>" + linkTeams + ":address"},
		{"never_observed", false, true, "unread", ""},
		{"known_empty", true, false, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateTmp(t)
			utcDays(t)
			db := newDB(t)
			root := outlookRoot(t, "HxStore.hxd")
			o := outlookOpts(db, root)
			if tc.prior {
				putOutlookAccount(t, root, ownAddress)
				run(t, o)
				putTeamsProfile(t, db, ownAddress)
				run(t, o)
			}
			putExtendedIdentityStore(t, root, tc.unknown)
			o.FullRead = true
			r, _ := run(t, o)
			if sourceKeyed(t, r, "outlook|Main").Status != StatusOK {
				t.Fatalf("new event layout was not applied: %+v", r.Sources)
			}
			if got := identity(t, db); got != tc.wantIdentity {
				t.Errorf("unknown identity became observed empty: %q, want %q", got, tc.wantIdentity)
			}
			if got := activeLinks(t, db); got != tc.wantLink {
				t.Errorf("unproved account change altered link: %q, want %q", got, tc.wantLink)
			}
		})
	}
}
