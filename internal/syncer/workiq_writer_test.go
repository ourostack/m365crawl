package syncer

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

type shortWriterWaitContext struct {
	context.Context
	waited chan struct{}
	once   sync.Once
}

// Capture the actual policy before TestMain replaces it for portable fixtures.
var nativeOutlookMailAdmission = outlookMailSupported

func TestSyncNativeMailAdmissionKeepsThePlatformOutcome(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	old := outlookMailSupported
	outlookMailSupported = nativeOutlookMailAdmission
	t.Cleanup(func() { outlookMailSupported = old })
	db := newDB(t)
	report, _ := run(t, outlookOpts(db, mailRoot(t)))
	mail := sourceKeyed(t, report, "outlook|Main|mail")
	if runtime.GOOS == "windows" {
		if mail.Status != StatusUnavailable || mail.Error == nil || mail.Error.Code != "mail_unsupported_platform" ||
			count(t, db, `select count(*) from mail_messages`) != 0 {
			t.Fatal("native unsupported mail policy read payloads or lost its distinct outcome")
		}
	} else if mail.Status != StatusOK || mail.Counts.Mail.Added != 2 || count(t, db, `select count(*) from mail_messages`) != 2 {
		t.Fatal("native supported mail policy failed to admit exactly the synthetic mailbox")
	}
}

func (c *shortWriterWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waited) })
	return c.Context.Done()
}

func TestSyncWaitsForOwnedShortWriterBeforeDiscovery(t *testing.T) {
	db := newDB(t)
	base, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx := &shortWriterWaitContext{Context: base, waited: make(chan struct{})}
	short, err := store.AcquireWriter(base, db, store.WriterShortServer)
	if err != nil {
		t.Fatal(err)
	}
	defer short()
	result := make(chan error, 1)
	go func() { _, _, err := Run(ctx, Options{Root: fixtureRoot, DBPath: db}); result <- err }()
	select {
	case err := <-result:
		t.Fatalf("ordinary Run refused before the held short-writer role could yield: %v", err)
	case <-base.Done():
		cancel()
		short()
		<-result
		t.Fatal("ordinary Run did not enter its bounded writer wait")
	case <-ctx.waited:
		short()
		if err := <-result; err != nil {
			t.Fatalf("ordinary synthetic sync failed after reserved writer acquired: %v", err)
		}
	}
}
