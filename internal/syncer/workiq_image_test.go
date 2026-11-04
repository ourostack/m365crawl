package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/imageexport"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func workIQImageFixture(t *testing.T, mode string) WorkIQImageOptions {
	t.Helper()
	isolateTmp(t)
	db := newDB(t)
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyAccount(context.Background(), teamsdesktop.Account{TenantID: "synthetic-tenant", UserID: "synthetic-user"}); err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyMessages(context.Background(), []teamsdesktop.Message{{
		TenantID: "synthetic-tenant", UserID: "synthetic-user", ConversationID: "chat", ID: "image-message",
		MessageType: "Text", ContentType: "html",
		ContentHTML: `<img itemtype="http://schema.skype.com/AMSImage" itemid="synthetic-image" src="https://example.invalid/synthetic-source">`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkIQEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	transcript := workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_IMAGE_CASE", mode)
	t.Setenv("WORKIQ_DOCUMENT_TRANSCRIPT", transcript)
	return WorkIQImageOptions{DBPath: db, Account: "synthetic-tenant/synthetic-user", ConversationID: "chat",
		MessageID: "image-message", ImageID: "synthetic-image", Output: filepath.Join(t.TempDir(), "selected.png")}
}

func TestFetchChatImageCloseBeforePublication(t *testing.T) {
	for _, closeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful-close", true: "failed-close"}[closeFails], func(t *testing.T) {
			options := workIQImageFixture(t, "good")
			prev := workIQClose
			closed := false
			fault := errors.New("private native close detail")
			workIQClose = func(client *workiq.Client) error {
				if _, err := os.Stat(options.Output); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("destination visible before native close", err)
				}
				err := prev(client)
				closed = true
				if closeFails {
					err = errors.Join(err, fault)
				}
				return err
			}
			t.Cleanup(func() { workIQClose = prev })
			report, err := FetchWorkIQChatImage(context.Background(), options)
			if !closed {
				t.Fatal("selected native client never closed")
			}
			if closeFails {
				if !errors.Is(err, fault) || report.Status != "failed" ||
					!slices.Contains(report.AdditionalCodes, "image_native_close_failed") ||
					report.Effect.FilePublished == nil || *report.Effect.FilePublished {
					t.Fatalf("failed close authorized output or lost cause/effect: %+v %v", report, err)
				}
				if entries, err := os.ReadDir(filepath.Dir(options.Output)); err != nil || len(entries) != 0 {
					t.Fatal("failed native close left output or reservation", entries, err)
				}
				return
			}
			if err != nil || report.Status != "acquired" || report.Effect.FilePublished == nil || !*report.Effect.FilePublished {
				t.Fatalf("closed verified acquisition was not published: %+v %v", report, err)
			}
		})
	}
}

func TestImageOutcomeAdditionalFaults(t *testing.T) {
	options := workIQImageFixture(t, "blob-failure")
	prevClose, prevReserve, prevAbort := workIQClose, imageReserve, imageAbort
	closeFault := errors.New("private close failure https://example.invalid/?sig=private-ticket")
	workIQClose = func(client *workiq.Client) error {
		return errors.Join(prevClose(client), closeFault)
	}
	var temp string
	imageReserve = func(ctx context.Context, output string) (*imageexport.Reservation, imageexport.Effect, error) {
		reserved, effect, err := prevReserve(ctx, output)
		if err == nil && effect.RetainedTemp != nil {
			temp = effect.RetainedTemp.LastKnownPath
		}
		return reserved, effect, err
	}
	held := filepath.Join(filepath.Dir(options.Output), "held-original-temp")
	imageAbort = func(reservation *imageexport.Reservation) (imageexport.Effect, error) {
		if temp != "" {
			if err := os.Rename(temp, held); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(temp, []byte("different test-owned object"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return prevAbort(reservation)
	}
	t.Cleanup(func() { workIQClose, imageReserve, imageAbort = prevClose, prevReserve, prevAbort })
	report, err := FetchWorkIQChatImage(context.Background(), options)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_failed" || report.Code != "workiq_failed" ||
		!errors.Is(err, closeFault) || report.Status != "failed" {
		t.Fatalf("secondary close/cleanup replaced primary native error: %+v %v", report, err)
	}
	if !slices.Contains(report.AdditionalCodes, "image_native_close_failed") ||
		!slices.Contains(report.AdditionalCodes, "image_output_cleanup_ownership_unknown") {
		t.Fatalf("additional failures were hidden: %+v", report)
	}
	if report.Effect.RetainedTemp == nil || report.Effect.RetainedTemp.State != "empty" ||
		report.Effect.RetainedTemp.LastKnownPath != temp || report.Effect.RetainedTemp.LocatorVerified == nil ||
		*report.Effect.RetainedTemp.LocatorVerified {
		t.Fatalf("retained temp lost truthful identity uncertainty: %+v", report.Effect)
	}
	data, readErr := os.ReadFile(temp)
	if readErr != nil || string(data) != "different test-owned object" {
		t.Fatal("abort removed another object", readErr)
	}
	if _, err := os.Stat(held); err != nil {
		t.Fatal("original reserved evidence was not retained", err)
	}
}

func TestFetchChatImagePublicationIdentityFailure(t *testing.T) {
	for _, tc := range []struct {
		name, phaseCode string
	}{
		{"parent-moved", "image_output_changed"},
		{"reservation-closed", "image_output_closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := workIQImageFixture(t, "good")
			parent := filepath.Dir(options.Output)
			observedParent := parent
			prev := imagePublish
			imagePublish = func(reservation *imageexport.Reservation, ctx context.Context, data []byte) (imageexport.Effect, error) {
				if tc.name == "parent-moved" {
					observedParent = parent + "-moved"
					if err := os.Rename(parent, observedParent); err != nil {
						t.Fatal(err)
					}
				} else if _, err := reservation.Abort(); err != nil {
					t.Fatal(err)
				}
				return prev(reservation, ctx, data)
			}
			t.Cleanup(func() { imagePublish = prev })
			report, err := FetchWorkIQChatImage(context.Background(), options)
			var coded *errs.Coded
			if !errors.As(err, &coded) || coded.Code != tc.phaseCode ||
				report.Code != "image_output_failed" || report.Status != "failed" ||
				!slices.Contains(report.AdditionalCodes, tc.phaseCode) || report.FetchedAt == nil {
				t.Fatalf("publication identity failure escaped the stable runtime outcome: %+v %v", report, err)
			}
			if report.Effect.FilePublished == nil || *report.Effect.FilePublished ||
				report.Effect.OutputVerified == nil || *report.Effect.OutputVerified {
				t.Fatalf("failed publication invented file success or uncertainty: %+v", report.Effect)
			}
			if entries, err := os.ReadDir(observedParent); err != nil || len(entries) != 0 {
				t.Fatal("owned failed reservation was not cleaned", entries, err)
			}
		})
	}
}
