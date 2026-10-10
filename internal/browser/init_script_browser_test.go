package browser

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitScriptActualNavigationAndRemoval(t *testing.T) {
	exe, kind, err := Find("edge")
	if err != nil {
		if os.Getenv("M365CRAWL_REQUIRE_BROWSER") == "1" {
			t.Fatal(err)
		}
		t.Skip("synthetic early script test requires Edge")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	b, err := Launch(ctx, LaunchOptions{Exe: exe, Kind: kind, Profile: filepath.Join(t.TempDir(), "archive", "profile"), Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	p, err := b.Page(ctx)
	if err != nil {
		t.Fatal(err)
	}
	page := scriptMethods(t, p)
	id, err := page.AddInitScript(ctx, `window.fixtureEarly = document.readyState === "loading";`)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Navigate(ctx, "data:text/html,<html><body>first synthetic document</body></html>"); err != nil {
		t.Fatal(err)
	}
	var early bool
	if err := p.Eval(ctx, "window.fixtureEarly === true", &early); err != nil || !early {
		t.Fatalf("not installed before app load: %t, %v", early, err)
	}
	if err := page.RemoveInitScript(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := p.Navigate(ctx, "data:text/html,<html><body>second synthetic document</body></html>"); err != nil {
		t.Fatal(err)
	}
	var absent bool
	if err := p.Eval(ctx, `typeof window.fixtureEarly === "undefined"`, &absent); err != nil || !absent {
		t.Fatalf("removed script installed again: %t, %v", absent, err)
	}
}
