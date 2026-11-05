//go:build darwin

package syncer

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestFetchChatImageDarwinInheritedACLRefusesBeforeNative(t *testing.T) {
	options := workIQImageFixture(t, "good")
	dir := filepath.Dir(options.Output)
	// #nosec G204 -- Fixed native tool and ACL, exact output parent from owned synthetic fixture, no shell.
	if output, err := osexec.Command("/bin/chmod", "+a", "group:staff allow read,file_inherit,only_inherit", dir).CombinedOutput(); err != nil {
		t.Fatal("controlled native inherited ACL fixture failed", err, string(output))
	}
	old := workIQOpen
	t.Cleanup(func() { workIQOpen = old })
	workIQOpen = func(context.Context, workiq.Invocation, string) (*workiq.Client, error) {
		t.Fatal("unsafe Darwin inherited ACL reached native acquisition")
		return nil, nil
	}
	report, err := FetchWorkIQChatImage(context.Background(), options)
	if err == nil || report.Code != "image_output_unsafe" || report.Status != "refused" ||
		report.Effect.FilePublished == nil || *report.Effect.FilePublished || report.Effect.RetainedTemp != nil {
		t.Fatal("inherited ACL did not refuse before reservation/native acquisition", report, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatal("inherited ACL refusal left output or scratch", entries, err)
	}
}
