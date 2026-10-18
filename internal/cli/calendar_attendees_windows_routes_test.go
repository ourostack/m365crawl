package cli

import (
	"reflect"
	"testing"
)

func TestAttendeeWindowsCommandConsumerPreservesNativePaths(t *testing.T) {
	oldQuote := quoteArg
	t.Cleanup(func() { quoteArg = oldQuote })
	quoteArg = quoteFor("windows")
	db := `D:\a\_temp\archive.db`
	root := `D:\synthetic\saved 'root`
	teams := `D:\a\m365crawl\testdata\teams-fixture\EBWebView`
	command := archiveScopedCommand(db, "sync", "--outlook-root", root, "--teams-root", teams)
	got := attendeeCommandWordsFor(t, command, "windows")
	want := []string{"m365crawl", "--db", db, "--max-age", "0", "sync", "--outlook-root", root, "--teams-root", teams}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("test consumer damaged actual Windows follow-up arguments", got, want)
	}
}
