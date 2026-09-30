package cli

import (
	"testing"
	"time"
)

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)
	loc := time.FixedZone("x", -7*3600)
	cases := []struct {
		in   string
		want time.Time
	}{
		{"2026-09-01T10:00:00Z", time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)},
		{"2026-09-01T10:00:00+02:00", time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)},
		{"2026-09-01", time.Date(2026, 9, 1, 0, 0, 0, 0, loc)},
		{"90m", now.Add(-90 * time.Minute)},
		{"24h", now.Add(-24 * time.Hour)},
		{"7d", now.Add(-7 * 24 * time.Hour)},
		{"2w", now.Add(-14 * 24 * time.Hour)},
		{"1h30m", now.Add(-90 * time.Minute)},
	}
	for _, c := range cases {
		got, err := parseWhen(c.in, now, loc)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("%q: got %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "yesterday", "-5m", "7x", "2026-13-01", "0"} {
		if _, err := parseWhen(bad, now, loc); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestParseMaxAge(t *testing.T) {
	for in, want := range map[string]time.Duration{"15m": 15 * time.Minute, "0": 0, "1d": 24 * time.Hour, "1ns": time.Nanosecond, "2w": 14 * 24 * time.Hour} {
		got, err := parseMaxAge(in)
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "soon", "-1m"} {
		if _, err := parseMaxAge(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	if s, cut := truncateRunes("héllo wörld", 5); s != "héllo…" || !cut {
		t.Fatalf("%q %v", s, cut)
	}
	if s, cut := truncateRunes("short", 5); s != "short" || cut {
		t.Fatalf("%q %v", s, cut)
	}
	if s, cut := truncateRunes("anything", 0); s != "anything" || cut {
		t.Fatalf("0 disables: %q %v", s, cut)
	}
	if s, _ := truncateRunes("👨‍👩‍👧x", 1); s != "👨…" {
		t.Fatalf("rune based: %q", s)
	}
}

func TestParseDeepLink(t *testing.T) {
	conv, root, err := parseThreadTarget("https://teams.microsoft.com/l/message/19%3Aabc%40thread.tacv2/1700000046000?tenantId=t&parentMessageId=1700000045000&context=%7B%7D", "")
	if err != nil || conv != "19:abc@thread.tacv2" || root != "1700000045000" {
		t.Fatalf("%q %q %v", conv, root, err)
	}
	conv, root, err = parseThreadTarget("https://teams.microsoft.com/l/message/19%3Aabc%40thread.v2/17?tenantId=t", "")
	if err != nil || conv != "19:abc@thread.v2" || root != "17" {
		t.Fatalf("%q %q %v", conv, root, err)
	}
	if _, _, err := parseThreadTarget("https://example.com/x", ""); err == nil {
		t.Fatal("non-Teams link must fail")
	}
	if _, _, err := parseThreadTarget("19:abc@thread.v2", ""); err == nil {
		t.Fatal("conversation without a root must fail")
	}
	conv, root, err = parseThreadTarget("19:abc@thread.v2", "5")
	if err != nil || conv != "19:abc@thread.v2" || root != "5" {
		t.Fatalf("%q %q %v", conv, root, err)
	}
}
