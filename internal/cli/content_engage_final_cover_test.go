package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestEngageFinalGuardPaths(t *testing.T) {
	if _, err := earlyEngagePage(nil); err == nil {
		t.Fatal("nil page accepted")
	}
	cmd := &contentSigninCmd{URL: store.EngageHomeURL, UserAgreed: true}
	rt := &runtime{ctx: context.Background(), g: &Globals{}, fields: []string{"unsupported"}, stdout: io.Discard, stderr: io.Discard}
	if err := cmd.Run(rt); err == nil {
		t.Fatal("unsupported signin fields admitted")
	}
	rt.fields = nil
	saved := findBrowser
	t.Cleanup(func() { findBrowser = saved })
	findBrowser = func(string) (string, browser.Kind, error) { return "", "", errors.New("synthetic absent browser") }
	if err := cmd.Run(rt); err == nil {
		t.Fatal("absent signin browser admitted")
	}
}

// Exercises real read-only SQL cancellation, not a fabricated Store error.
func TestEngageFullSQLCancellationDuringRead(t *testing.T) {
	e := cloudCLIEnv(t)
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var out, stderr bytes.Buffer
	code := runCLI(ctx, []string{"--db", e.db, "--teams-root", e.root, "--no-sync", "sql",
		"WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT sum(x) FROM n"}, &out, &stderr)
	if code == 0 || ctx.Err() == nil {
		t.Fatal("cancelled full SQL falsely succeeded")
	}
}
