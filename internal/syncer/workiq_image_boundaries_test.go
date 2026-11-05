package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/imageexport"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsimage"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestFetchChatImageInvalidOptionsAvoidArchiveAndOutput(t *testing.T) {
	oldRead, oldAbs := workIQRead, imageAbs
	t.Cleanup(func() { workIQRead, imageAbs = oldRead, oldAbs })
	workIQRead = func(context.Context, string, func(*store.Store) error) error {
		t.Fatal("invalid options accessed the archive")
		return nil
	}
	base := WorkIQImageOptions{ConversationID: "chat", MessageID: "message", ImageID: "image", Output: "/synthetic/new.png"}
	for _, mutate := range []func(*WorkIQImageOptions){
		func(o *WorkIQImageOptions) { o.ConversationID = "bad|chat" },
		func(o *WorkIQImageOptions) { o.MessageID = "" },
		func(o *WorkIQImageOptions) { o.ImageID = "bad\nimage" },
		func(o *WorkIQImageOptions) { o.Output = "" },
		func(o *WorkIQImageOptions) { o.Output = "bad\npath" },
		func(o *WorkIQImageOptions) { o.Account = "not-a-tenant-user" },
		func(o *WorkIQImageOptions) { o.Account = "tenant/bad|user" },
	} {
		options := base
		mutate(&options)
		report, err := FetchWorkIQChatImage(context.Background(), options)
		var coded *errs.Coded
		if !errors.As(err, &coded) || coded.Exit != errs.ExitUsage ||
			report.Status != "refused" || report.Effect.FilePublished == nil || *report.Effect.FilePublished {
			t.Fatal("invalid options lost their pre-acquisition refusal", report, err)
		}
	}
	sentinel := errors.New("synthetic absolute-path failure")
	imageAbs = func(string) (string, error) { return "", sentinel }
	report, err := FetchWorkIQChatImage(context.Background(), base)
	if !errors.Is(err, sentinel) || report.Code != "image_output_unsafe" || report.Status != "refused" {
		t.Fatal("output resolution failure lost its cause or refused outcome", report, err)
	}
}

func TestFetchChatImageCachedAndOptionalReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name, statement, account, code string
		boundary                       int
		closeReader                    bool
	}{
		{"accounts-query", "", "", "image_acquisition_failed", 1, true},
		{"ambiguous-account", "insert into accounts(tenant_id,user_id) values('second-tenant','second-user')", "", "usage", 1, false},
		{"unknown-account", "", "absent/user", "image_message_unknown", 1, false},
		{"invalid-archived-account", "update accounts set tenant_id='bad|tenant'", "", "image_message_unknown", 1, false},
		{"message-query", "drop table messages", "synthetic-tenant/synthetic-user", "image_acquisition_failed", 1, false},
		{"body-query", "alter table messages drop column content_html", "synthetic-tenant/synthetic-user", "image_acquisition_failed", 1, false},
		{"empty-body", "update messages set content_html=''", "synthetic-tenant/synthetic-user", "image_body_unavailable", 1, false},
		{"missing-message", "update messages set id='other-message'", "synthetic-tenant/synthetic-user", "image_message_unknown", 1, false},
		{"optional-query", "", "synthetic-tenant/synthetic-user", "image_acquisition_failed", 2, true},
		{"disabled", "update meta set value='0' where key='workiq_enabled'", "synthetic-tenant/synthetic-user", "workiq_disabled", 2, false},
		{"optional-accounts-query", "drop table accounts", "synthetic-tenant/synthetic-user", "image_acquisition_failed", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := workIQImageFixture(t, "good")
			options.Account = tc.account
			original := workIQRead
			t.Cleanup(func() { workIQRead = original })
			reads := 0
			workIQRead = func(ctx context.Context, path string, operation func(*store.Store) error) error {
				reads++
				return original(ctx, path, func(reader *store.Store) error {
					if reads == tc.boundary {
						if tc.closeReader {
							if err := reader.Close(); err != nil {
								t.Fatal(err)
							}
						} else if tc.statement != "" {
							execSQL(t, path, tc.statement)
						}
					}
					return operation(reader)
				})
			}
			report, err := FetchWorkIQChatImage(context.Background(), options)
			if err == nil || report.Code != tc.code || report.Effect.FilePublished == nil || *report.Effect.FilePublished {
				t.Fatal("archive/admission failure became successful acquisition", report, err)
			}
			if entries, err := os.ReadDir(filepath.Dir(options.Output)); err != nil || len(entries) != 0 {
				t.Fatal("failed archive/admission left output or reservation", entries, err)
			}
		})
	}
	t.Run("no-archive", func(t *testing.T) {
		options := workIQImageFixture(t, "good")
		options.DBPath = filepath.Join(t.TempDir(), "absent.db")
		if report, err := FetchWorkIQChatImage(context.Background(), options); err == nil || report.Code != "image_message_unknown" {
			t.Fatal("absent archive became empty successful selection", report, err)
		}
	})
}

func TestFetchChatImageInspectionAndOpenFailure(t *testing.T) {
	for _, boundary := range []string{"inspection", "open", "cancelled-read"} {
		t.Run(boundary, func(t *testing.T) {
			options := workIQImageFixture(t, "good")
			oldInspect, oldOpen, oldRead := imageInspectHTML, workIQOpen, workIQRead
			t.Cleanup(func() { imageInspectHTML, workIQOpen, workIQRead = oldInspect, oldOpen, oldRead })
			sentinel := errors.New("synthetic acquisition boundary failure")
			want := "image_acquisition_failed"
			switch boundary {
			case "inspection":
				imageInspectHTML = func(context.Context, string) (teamsimage.Inspection, error) {
					return teamsimage.Inspection{}, sentinel
				}
			case "open":
				workIQOpen = func(context.Context, workiq.Invocation, string) (*workiq.Client, error) { return nil, sentinel }
			case "cancelled-read":
				sentinel = context.Canceled
				want = "interrupted"
				workIQRead = func(context.Context, string, func(*store.Store) error) error { return sentinel }
			}
			report, err := FetchWorkIQChatImage(context.Background(), options)
			if !errors.Is(err, sentinel) || report.Code != want || report.Status != "failed" {
				t.Fatal("boundary failure lost its cause or stable runtime outcome", report, err)
			}
			if entries, err := os.ReadDir(filepath.Dir(options.Output)); err != nil || len(entries) != 0 {
				t.Fatal("failed boundary retained an owned reservation", entries, err)
			}
		})
	}
}

func TestFetchChatImageNativeRefusalsPreserveArchive(t *testing.T) {
	for _, mode := range []string{"wrong-user", "invalid-address", "wrong-tenant", "wrong-chat", "channel", "unknown-kind", "malformed-chat",
		"deleted", "unsafe-reference", "changed-reference", "changed-clock", "lost-clock", "deleted-after-blob",
		"invalid-bytes", "wrong-format", "blob-failure"} {
		t.Run(mode, func(t *testing.T) {
			options := workIQImageFixture(t, mode)
			before, err := os.ReadFile(options.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			report, err := FetchWorkIQChatImage(context.Background(), options)
			if err == nil || report.Status == "acquired" || report.Effect.FilePublished == nil || *report.Effect.FilePublished {
				t.Fatal("native refusal authorized image publication", report, err)
			}
			after, err := os.ReadFile(options.DBPath)
			if err != nil || !slices.Equal(before, after) {
				t.Fatal("refused acquisition changed archive bytes or source state", err)
			}
			if entries, err := os.ReadDir(filepath.Dir(options.Output)); err != nil || len(entries) != 0 {
				t.Fatal("native refusal left output or owned scratch", entries, err)
			}
		})
	}
	for _, from := range []int{2, 3, 4} {
		t.Run(fmt.Sprintf("native-fetch-%d", from), func(t *testing.T) {
			options := workIQImageFixture(t, "good")
			t.Setenv("WORKIQ_ACQUISITION_FAIL_FROM", fmt.Sprint(from))
			t.Setenv("WORKIQ_ACQUISITION_FAIL_COUNTER", filepath.Join(t.TempDir(), "calls"))
			report, err := FetchWorkIQChatImage(context.Background(), options)
			if err == nil || report.Code != "workiq_failed" || report.Effect.FilePublished == nil || *report.Effect.FilePublished {
				t.Fatal("failed chat/message/recheck fetch authorized output", report, err)
			}
		})
	}
}

func TestImageOutcomeNormalizesAdmissionAndUncertainty(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"image_output_invalid", "image_output_unsafe"},
		{"image_output_unprotected", "image_output_unsafe"},
		{"image_format_unsupported", "image_kind_unsupported"},
		{"image_message_unbindable", "image_reference_unsupported"},
		{"image_bytes_invalid", "image_invalid_bytes"},
		{"image_output_reserve_failed", "image_output_failed"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			options := workIQImageFixture(t, "good")
			old := imageReserve
			t.Cleanup(func() { imageReserve = old })
			imageReserve = func(context.Context, string) (*imageexport.Reservation, imageexport.Effect, error) {
				value := false
				return nil, imageexport.Effect{FilePublished: &value, OutputVerified: &value},
					&errs.Coded{Code: tc.input, Exit: errs.ExitEnvironment}
			}
			report, err := FetchWorkIQChatImage(context.Background(), options)
			if err == nil || report.Code != tc.want || !slices.Contains(report.AdditionalCodes, tc.input) {
				t.Fatal("primitive refusal escaped stable acquisition normalization", report, err)
			}
		})
	}
	t.Run("successful-publisher-without-proof", func(t *testing.T) {
		options := workIQImageFixture(t, "good")
		old := imagePublish
		t.Cleanup(func() { imagePublish = old })
		imagePublish = func(*imageexport.Reservation, context.Context, []byte) (imageexport.Effect, error) {
			return imageexport.Effect{}, nil
		}
		report, err := FetchWorkIQChatImage(context.Background(), options)
		if err == nil || report.Code != "image_output_uncertain" || report.Status != "failed" {
			t.Fatal("successful publisher without positive publication proof was accepted", report, err)
		}
	})
	t.Run("published-output-with-uncertain-final-verification", func(t *testing.T) {
		options := workIQImageFixture(t, "good")
		oldPublish, oldAbort := imagePublish, imageAbort
		t.Cleanup(func() { imagePublish, imageAbort = oldPublish, oldAbort })
		sentinel := errors.New("synthetic post-publication failure")
		imagePublish = func(reservation *imageexport.Reservation, ctx context.Context, data []byte) (imageexport.Effect, error) {
			effect, err := oldPublish(reservation, ctx, data)
			return effect, errors.Join(err, sentinel)
		}
		imageAbort = func(reservation *imageexport.Reservation) (imageexport.Effect, error) {
			effect, err := oldAbort(reservation)
			effect.OutputVerified = nil
			return effect, err
		}
		report, err := FetchWorkIQChatImage(context.Background(), options)
		if !errors.Is(err, sentinel) || report.Code != "image_output_uncertain" || report.Status != "failed" ||
			report.Effect.FilePublished == nil || !*report.Effect.FilePublished || report.Effect.OutputVerified != nil {
			t.Fatal("uncertain final verification lost actual publication or became success", report, err)
		}
		if data, err := os.ReadFile(options.Output); err != nil || len(data) < 8 ||
			string(data[:8]) != "\x89PNG\r\n\x1a\n" {
			t.Fatal("uncertain final verification removed the published PNG", err)
		}
	})
}
