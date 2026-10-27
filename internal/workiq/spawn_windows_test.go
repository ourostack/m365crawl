//go:build windows

package workiq

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkIQWindowsCmdRoundTrip(t *testing.T) {
	useStub(t, "")
	exe := os.Getenv("M365CRAWL_WORKIQ_BIN")
	root := filepath.Join(t.TempDir(), "space containing")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(root, "workiq.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\n\""+exe+"\" %*\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("M365CRAWL_WORKIQ_BIN", shim)
	client, err := Open(context.Background(), Invocation{}, "test.user@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Fetch(context.Background(), "/me"); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"bad&path.cmd", "bad%PATH%.cmd", "bad!path.cmd", "bad^path.cmd", "bad\"path.cmd"} {
		t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(root, path))
		_, err := Open(context.Background(), Invocation{}, "")
		requireCode(t, err, "workiq_binary_unsafe")
	}
}
