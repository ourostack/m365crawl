package syncer

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

func TestWorkIQExplicitUnknownProbeAccountRefusesBeforeAnySubprocess(t *testing.T) {
	for _, kind := range []string{"coverage", "status"} {
		db, _ := workIQAcquisitionArchive(t)
		transcript := workIQAcquisitionStub(t)
		seedWorkIQChatOwner(t, db)
		t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
		_, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats",
			Account: "unknown/user", Kind: kind})
		var coded *errs.Coded
		if !errors.As(err, &coded) || coded.Code != "workiq_chat_account_unmatched" {
			t.Fatal("an explicit unknown owner became checked positive source status", kind, err)
		}

		if _, err := os.Stat(transcript); !os.IsNotExist(err) {
			t.Fatal("unknown archive owner started a source subprocess")
		}
	}
}

func TestWorkIQUnknownMailProbeOwnerRetainsEnvironmentExit(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	_, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: "outlook/Unknown", Kind: "status"})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_mail_account_missing" || coded.Exit != errs.ExitEnvironment {
		t.Fatal("unknown mail account lost its documented environment refusal", err)
	}
}
