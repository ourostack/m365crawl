package syncer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

func TestWorkIQExplicitChatOwnerMismatchStopsBeforeContentDiscovery(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	transcript := workIQAcquisitionStub(t)
	owner := seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_ME", "unbound-other")
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	_, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats",
		Account: owner.TenantID + "/" + owner.UserID})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_identity_mismatch" {
		t.Fatal("explicit archived user mismatch became empty-success chat discovery", err)
	}
	raw, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic fetch transcript returned by the test helper.
	if err != nil || strings.Contains(string(raw), "/me/chats") {
		t.Fatal("mismatched explicit user reached another user's chat listing", err)
	}
}

func TestWorkIQExplicitBootstrapVerifiesTheChosenAddressWasHonored(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	_, err := openRaw(t, db).Exec(`insert into meta(key,value) values(?,?)`,
		"outlook_identity:"+account, "test.user@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKIQ_ACQUISITION_ME", "pinned-other-address")
	_, err = workIQBootstrap(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_identity_mismatch" {
		t.Fatal("pinned bootstrap returned a different account as verified identity", err)
	}
}
