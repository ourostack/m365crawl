package syncer

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/imageexport"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/teamsimage"
	"github.com/ourostack/m365crawl/internal/workiq"
)

type WorkIQImageOptions struct {
	DBPath, Account, ConversationID, MessageID, ImageID, Output string
	Invocation                                                  workiq.Invocation
}

type WorkIQImageReport struct {
	Account, ConversationID, MessageID, ImageID, Output string
	Status, Code                                        string
	AdditionalCodes                                     []string
	Info                                                teamsimage.PNGInfo
	NativeMessageModifiedAt                             *time.Time
	NativeBodyObservedAt                                *time.Time
	VerificationObservedAt                              *time.Time
	FetchedAt                                           *time.Time
	Effect                                              imageexport.Effect
}

var imageReserve = imageexport.Reserve
var imagePublish = (*imageexport.Reservation).Publish
var imageAbort = (*imageexport.Reservation).Abort

func FetchWorkIQChatImage(ctx context.Context, options WorkIQImageOptions) (report WorkIQImageReport, err error) {
	notPublished, notVerified := false, false
	report = WorkIQImageReport{
		Account: options.Account, ConversationID: options.ConversationID, MessageID: options.MessageID,
		ImageID: options.ImageID, Output: options.Output,
		Effect: imageexport.Effect{Phase: "admission", FilePublished: &notPublished, OutputVerified: &notVerified},
	}
	var reservation *imageexport.Reservation
	defer func() {
		err = imageCause(err)
		if reservation != nil {
			effect, cleanupErr := imageAbort(reservation)
			report.Effect = effect
			err = errors.Join(err, cleanupErr)
		}
		report.finish(err)
	}()
	if !teamsimage.ValidComponent(options.ConversationID) || !teamsimage.ValidComponent(options.MessageID) ||
		!teamsimage.ValidComponent(options.ImageID) || options.Output == "" || strings.ContainsAny(options.Output, "\x00\r\n") {
		return report, errs.Usage("image fetch requires bounded exact conversation/message/image IDs and an explicit output file")
	}
	if options.Account != "" {
		parts := strings.Split(options.Account, "/")
		if len(parts) != 2 || !teamsimage.ValidComponent(parts[0]) || !teamsimage.ValidComponent(parts[1]) {
			return report, errs.Usage("image fetch requires an exact Teams tenant/user account")
		}
	}
	report.Output, err = filepath.Abs(options.Output)
	if err != nil {
		return report, errors.Join(imageFailure("image_output_unsafe", errs.ExitEnvironment), err)
	}
	if err := report.selectCached(ctx, options); err != nil {
		return report, err
	}
	reservation, report.Effect, err = imageReserve(ctx, report.Output)
	if err != nil {
		return report, err
	}
	var fact workiq.AccountFact
	if err := workIQRead(ctx, options.DBPath, func(s *store.Store) error {
		enabled, err := s.WorkIQEnabled(ctx)
		if err != nil {
			return err
		}
		if !enabled {
			return workIQFailure("workiq_disabled")
		}
		facts, err := s.WorkIQAccounts(ctx)
		if err == nil {
			fact, err = documentAccount(facts, report.Account)
		}
		return err
	}); err != nil {
		return report, err
	}
	client, user, err := workIQOpenUser(ctx, options.Invocation, "")
	if err != nil {
		return report, err
	}
	data, acquisitionErr := report.acquire(ctx, client, user, fact)
	acquisitionErr = imageCause(acquisitionErr)
	closeErr := workIQClose(client)
	if closeErr != nil {
		report.AdditionalCodes = append(report.AdditionalCodes, "image_native_close_failed")
		closeErr = errors.Join(imageFailure("image_native_close_failed", errs.ExitRuntime), closeErr)
	}
	if err := errors.Join(acquisitionErr, closeErr); err != nil {
		return report, err
	}
	report.Effect, err = imagePublish(reservation, ctx, data)
	if err == nil && (report.Effect.FilePublished == nil || !*report.Effect.FilePublished ||
		report.Effect.OutputVerified == nil || !*report.Effect.OutputVerified) {
		err = imageFailure("image_output_uncertain", errs.ExitRuntime)
	}
	return report, err
}

func (r *WorkIQImageReport) selectCached(ctx context.Context, options WorkIQImageOptions) error {
	err := workIQRead(ctx, options.DBPath, func(s *store.Store) error {
		accounts, err := s.TeamsAccounts(ctx)
		if err != nil {
			return err
		}
		if options.Account == "" && len(accounts) != 1 {
			return errs.Usage("image fetch requires --account when the archive does not have exactly one Teams account")
		}
		var selected *teamsdesktop.Account
		for i := range accounts {
			a := &accounts[i]
			if options.Account == "" || options.Account == a.TenantID+"/"+a.UserID {
				selected = a
				break
			}
		}
		if selected == nil {
			return imageFailure("image_message_unknown", errs.ExitEnvironment)
		}
		if !teamsimage.ValidComponent(selected.TenantID) || !teamsimage.ValidComponent(selected.UserID) {
			return imageFailure("image_message_unknown", errs.ExitEnvironment)
		}
		r.Account = selected.TenantID + "/" + selected.UserID
		key := store.MessageKey{TenantID: selected.TenantID, UserID: selected.UserID,
			ConversationID: r.ConversationID, ID: r.MessageID}
		exists, err := s.HasMessage(ctx, key)
		if err != nil {
			return err
		}
		if !exists {
			return imageFailure("image_message_unknown", errs.ExitEnvironment)
		}
		bodies, err := s.MessageHTML(ctx, []store.MessageKey{key})
		if err != nil {
			return err
		}
		if strings.TrimSpace(bodies[key]) == "" {
			return imageFailure("image_body_unavailable", errs.ExitEnvironment)
		}
		inspection, err := teamsimage.InspectHTML(ctx, bodies[key])
		if err != nil {
			return err
		}
		_, err = teamsimage.Select(inspection, r.ImageID)
		return err
	})
	if errors.Is(err, store.ErrNoArchive) {
		return imageFailure("image_message_unknown", errs.ExitEnvironment)
	}
	return err
}

func (r *WorkIQImageReport) acquire(ctx context.Context, client *workiq.Client, user workiq.User, fact workiq.AccountFact) ([]byte, error) {
	if fact.UserID == "" || user.ID != fact.UserID {
		return nil, workIQFailure("workiq_identity_mismatch")
	}
	resource := "/chats/" + url.PathEscape(r.ConversationID)
	page, err := client.Fetch(ctx, resource)
	if err != nil {
		return nil, err
	}
	chat, err := workiq.DecodeChat(ctx, page.Data)
	if err != nil {
		return nil, err
	}
	if chat.ID != r.ConversationID {
		return nil, imageFailure("image_reference_unsupported", errs.ExitEnvironment)
	}
	if chat.Type != "oneOnOne" && chat.Type != "group" && chat.Type != "meeting" {
		return nil, imageFailure("image_kind_unsupported", errs.ExitEnvironment)
	}
	if _, err := workiq.SelectChatAccount(user, chat, []workiq.AccountFact{fact}, r.Account); err != nil {
		return nil, err
	}
	messagePath := resource + "/messages/" + url.PathEscape(r.MessageID)
	before, err := r.observe(ctx, client, messagePath)
	if err != nil {
		return nil, err
	}
	observed := workIQNow().UTC()
	r.NativeMessageModifiedAt, r.NativeBodyObservedAt = before.ModifiedAt, &observed
	ref, err := workiq.BindChatImage(before, r.ImageID)
	if err != nil {
		return nil, err
	}
	blob, err := client.FetchChatImageBlob(ctx, ref)
	if err != nil {
		return nil, err
	}
	r.Info, err = teamsimage.ValidatePNG(ctx, blob.Data, blob.ContentType)
	if err != nil {
		return nil, err
	}
	fetched := workIQNow().UTC()
	r.FetchedAt = &fetched
	after, err := r.observe(ctx, client, messagePath)
	if err != nil {
		return nil, err
	}
	verified := workIQNow().UTC()
	r.VerificationObservedAt = &verified
	if err := workiq.SameImageObservation(before, after, r.ImageID); err != nil {
		return nil, err
	}
	return blob.Data, nil
}

func (r *WorkIQImageReport) observe(ctx context.Context, client *workiq.Client, resource string) (workiq.ImageMessage, error) {
	page, err := client.Fetch(ctx, resource)
	if err != nil {
		return workiq.ImageMessage{}, err
	}
	return workiq.DecodeSelectedImageMessage(ctx, r.ConversationID, r.MessageID, page.Data)
}

func imageCause(err error) error {
	if err == nil {
		return nil
	}
	primary := err
	for {
		joined, ok := primary.(interface{ Unwrap() []error })
		if !ok || len(joined.Unwrap()) == 0 {
			break
		}
		primary = joined.Unwrap()[0]
	}
	var coded *errs.Coded
	if errors.As(primary, &coded) {
		return err
	}
	if errors.Is(primary, context.Canceled) {
		return errors.Join(imageFailure("interrupted", errs.ExitRuntime), err)
	}
	return errors.Join(imageFailure("image_acquisition_failed", errs.ExitRuntime), err)
}

func imageFailure(code string, exit int) *errs.Coded {
	return &errs.Coded{Code: code, Exit: exit,
		Message: "the selected image acquisition could not complete (" + code + ")",
		Fix:     "Check the exact account/message selection and acquisition receipt; retained files are never overwritten."}
}

func (r *WorkIQImageReport) finish(err error) {
	r.Status = "acquired"
	if err == nil {
		return
	}
	r.Status, r.Code = "failed", "image_acquisition_failed"
	var coded *errs.Coded
	if errors.As(err, &coded) {
		r.Code = coded.Code
		if (coded.Exit == errs.ExitEnvironment || coded.Exit == errs.ExitUsage) &&
			r.Effect.FilePublished != nil && !*r.Effect.FilePublished &&
			len(r.AdditionalCodes) == 0 && len(r.Effect.Codes) == 0 {
			r.Status = "refused"
		}
	}
	switch r.Code {
	case "image_format_unsupported":
		r.Code = "image_kind_unsupported"
	case "image_bytes_invalid":
		r.Code, r.Status = "image_invalid_bytes", "failed"
	case "image_message_unbindable":
		r.Code = "image_reference_unsupported"
	case "image_output_invalid", "image_output_unprotected":
		r.Code = "image_output_unsafe"
	case "image_output_write_failed", "image_output_sync_failed", "image_output_close_failed",
		"image_output_publish_failed", "image_output_open_failed", "image_output_reserve_failed",
		"image_output_changed", "image_output_closed":
		r.Code, r.Status = "image_output_failed", "failed"
	}
	if r.Effect.FilePublished == nil || *r.Effect.FilePublished &&
		(r.Effect.OutputVerified == nil || !*r.Effect.OutputVerified) {
		r.Code, r.Status = "image_output_uncertain", "failed"
	}
	for _, code := range append(imageErrorCodes(err), r.Effect.Codes...) {
		if code != r.Code && !slices.Contains(r.AdditionalCodes, code) {
			r.AdditionalCodes = append(r.AdditionalCodes, code)
		}
	}
}

func imageErrorCodes(err error) []string {
	if err == nil {
		return nil
	}
	var codes []string
	if coded, ok := err.(*errs.Coded); ok {
		codes = append(codes, coded.Code)
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, cause := range wrapped.Unwrap() {
			codes = append(codes, imageErrorCodes(cause)...)
		}
	case interface{ Unwrap() error }:
		codes = append(codes, imageErrorCodes(wrapped.Unwrap())...)
	}
	return codes
}
