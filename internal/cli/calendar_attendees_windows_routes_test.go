package cli

import (
	"reflect"
	"testing"
)

func TestAttendeeWindowsCommandConsumerPreservesNativePaths(t *testing.T) {
	oldQuote, oldPlatform := quoteArg, attendeeCommandPlatform
	t.Cleanup(func() { quoteArg, attendeeCommandPlatform = oldQuote, oldPlatform })
	quoteArg, attendeeCommandPlatform = quoteFor("windows"), "windows"
	db := `D:\a\_temp\archive.db`
	root := `D:\synthetic\saved 'root`
	teams := `D:\a\m365crawl\testdata\teams-fixture\EBWebView`
	command := archiveScopedCommand(db, "sync", "--outlook-root", root, "--teams-root", teams)
	got := attendeeCommandWords(t, command)
	want := []string{"m365crawl", "--db", db, "--max-age", "0", "sync", "--outlook-root", root, "--teams-root", teams}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("test consumer damaged actual Windows follow-up arguments", got, want)
	}
}
