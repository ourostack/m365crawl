package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/teamsimage"
)

const imageFetchHelp = "`content fetch image TARGET [MESSAGE_ID] --image-id ID --output FILE [--team-id TEAM]` explicitly exports one selected Teams PNG through the enabled read-only WorkIQ source. Use an exact message link alone or a conversation ID plus message ID. --account selects an archived Teams tenant/user; otherwise exactly one archived account is required. The cached HTML must be completely inspected and the exact image reference uniquely supported.\n" +
	"Without a team, only current oneOnOne/group/meeting chat messages are admitted. --team-id (or a message link's groupId) selects a channel root post: TARGET names the exact channel, and explicit/link team selectors must agree. Only standard/private channels with the same signed-in user and native tenant are admitted; shared/unknown channels and replies refuse. Both native message observations must name the exact team/channel. The native body must bind the same selected image to that exact message's hosted content; it is rechecked after acquisition. Both absent modification clocks remain null, not proof of freshness. No cached source URL, signed ticket, collection position or arbitrary URL is fetched.\n" +
	"PNG only: at most 4MiB raw, 16777216 pixels, 16384 per dimension and 64MiB decoded pixel storage. Complete nonanimated raster/CRC/IEND/EOF validation is required. No JPEG/GIF/WebP, channel replies, OCR or automatic media fetch. Other media remain not_acquired; this does not establish historical immutability or complete media coverage.\n" +
	"FILE needs an existing user-controlled parent. Windows additionally requires verified private user/SYSTEM effective and object-inheritable grants; no ACL is changed. Reserve privately before WorkIQ, close the native client before writing, and publish without overwrite or --force. Inspect image_acquisition on errors: a complete published file or uncertain retained temp may remain after cancellation, cleanup or stdout failure. Repeating against an existing FILE refuses before another download. Archive rows/config/cursors/source health are not changed. Native Windows service acquisition remains unqualified.\n"

type contentFetchImageCmd struct {
	contentImagesCmd
	ImageID string `required:"" help:"Exact supported image_id from the selected cached message's complete inventory."`
	Output  string `required:"" help:"Explicit new PNG file in an existing controlled parent; never overwritten."`
	TeamID  string `help:"Exact team ID for a channel root post; otherwise groupId from a message link selects the team."`
}

func (contentFetchImageCmd) Help() string { return imageFetchHelp }

var fetchWorkIQChatImage = syncer.FetchWorkIQChatImage

type imageFetchRetainedTemp struct {
	State           string `json:"state"`
	LastKnownPath   string `json:"last_known_path"`
	LocatorVerified *bool  `json:"locator_verified"`
}

type imageFetchResult struct {
	Status                  string                  `json:"status"`
	Code                    string                  `json:"code,omitempty"`
	AdditionalCodes         []string                `json:"additional_codes,omitempty"`
	Account                 string                  `json:"account"`
	TeamID                  string                  `json:"team_id,omitempty"`
	ConversationID          string                  `json:"conversation_id"`
	MessageID               string                  `json:"message_id"`
	ImageID                 string                  `json:"image_id"`
	Scope                   string                  `json:"scope"`
	OtherMedia              string                  `json:"other_media"`
	ContentType             string                  `json:"content_type,omitempty"`
	ByteSize                int                     `json:"byte_size,omitempty"`
	SHA256                  string                  `json:"sha256,omitempty"`
	Width                   int                     `json:"width,omitempty"`
	Height                  int                     `json:"height,omitempty"`
	NativeMessageModifiedAt *time.Time              `json:"native_message_modified_at"`
	NativeBodyObservedAt    *time.Time              `json:"native_body_observed_at"`
	VerificationObservedAt  *time.Time              `json:"verification_observed_at"`
	FetchedAt               *time.Time              `json:"fetched_at"`
	Output                  string                  `json:"output"`
	Phase                   string                  `json:"phase"`
	FilePublished           *bool                   `json:"file_published"`
	OutputVerified          *bool                   `json:"output_verified"`
	RetainedTemp            *imageFetchRetainedTemp `json:"retained_temp,omitempty"`
}

func imageFetchResultOf(r syncer.WorkIQImageReport) imageFetchResult {
	result := imageFetchResult{
		Status: r.Status, Code: r.Code, AdditionalCodes: r.AdditionalCodes,
		Account: r.Account, TeamID: r.TeamID, ConversationID: r.ConversationID, MessageID: r.MessageID, ImageID: r.ImageID,
		Scope: "selected_image", OtherMedia: "not_acquired", ContentType: r.Info.ContentType,
		ByteSize: r.Info.Size, SHA256: r.Info.SHA256, Width: r.Info.Width, Height: r.Info.Height,
		NativeMessageModifiedAt: r.NativeMessageModifiedAt, NativeBodyObservedAt: r.NativeBodyObservedAt,
		VerificationObservedAt: r.VerificationObservedAt, FetchedAt: r.FetchedAt,
		Output: r.Output, Phase: r.Effect.Phase, FilePublished: r.Effect.FilePublished, OutputVerified: r.Effect.OutputVerified,
	}
	if r.Effect.RetainedTemp != nil {
		temp := r.Effect.RetainedTemp
		result.RetainedTemp = &imageFetchRetainedTemp{State: temp.State, LastKnownPath: temp.LastKnownPath,
			LocatorVerified: temp.LocatorVerified}
	}
	return result
}

func (c *contentFetchImageCmd) Run(rt *runtime) error {
	conversation, message, err := c.selector(rt)
	if err != nil {
		return err
	}
	team, err := c.teamSelector(message)
	if err != nil {
		return err
	}
	if !teamsimage.ValidComponent(c.ImageID) {
		return errs.Usage("image fetch requires a bounded exact image_id, not a URL or hosted-content path")
	}
	account := ""
	if rt.account != nil {
		account = rt.account.TenantID + "/" + rt.account.UserID
	}
	report, err := fetchWorkIQChatImage(rt.ctx, syncer.WorkIQImageOptions{
		DBPath: rt.dbPath, Account: account, TeamID: team, ConversationID: conversation, MessageID: message,
		ImageID: c.ImageID, Output: c.Output, Invocation: rt.workIQInvocation(),
	})
	result := imageFetchResultOf(report)
	if err != nil {
		return &imageFetchError{result: result, cause: err}
	}
	err = rt.write(rt.cmd, result)
	if err == nil {
		err = rt.ctx.Err()
	}
	if err == nil {
		return nil
	}
	result.Status, result.Code = "failed", "image_receipt_failed"
	if errors.Is(err, context.Canceled) {
		result.Code = "interrupted"
	}
	return &imageFetchError{result: result, cause: err}
}

func (c *contentFetchImageCmd) teamSelector(message string) (string, error) {
	team := c.TeamID
	if isLink(c.Target) {
		// selector already validated the link. Scope-bearing queries parse strictly,
		// while chat links still ignore malformed unrelated query parameters.
		u, _ := url.Parse(cleanLink(c.Target))
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			scoped := team != ""
			for _, part := range strings.Split(u.RawQuery, "&") {
				key, _, _ := strings.Cut(part, "=")
				key, _ = url.QueryUnescape(key)
				scoped = scoped || key == "groupId"
			}
			if scoped {
				return "", errs.Usage("image fetch requires an unambiguous team selector in the message link")
			}
		}
		if values, ok := query["groupId"]; ok {
			if len(values) != 1 || values[0] == "" || team != "" && team != values[0] {
				return "", errs.Usage("--team-id and the message link groupId must select the same exact team")
			}
			team = values[0]
		}
		if parents := query["parentMessageId"]; team != "" &&
			(len(parents) > 1 || len(parents) == 1 && parents[0] != "" && parents[0] != message) {
			return "", errs.Usage("image fetch supports channel root posts, not reply links")
		}
	}
	if team != "" && (!teamsimage.ValidComponent(team) || strings.Contains(team, "%")) {
		return "", errs.Usage("image fetch requires a bounded exact team ID, not a URL or encoded path")
	}
	return team, nil
}

func (r imageFetchResult) renderImages(rt *runtime) error {
	encoder := json.NewEncoder(rt.stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(r)
}

type imageFetchError struct {
	result imageFetchResult
	cause  error
}

func (e *imageFetchError) Error() string {
	return "selected image acquisition failed (" + e.result.Code + ")"
}
func (e *imageFetchError) Unwrap() error { return e.cause }

func (rt *runtime) failImage(failure *imageFetchError) int {
	coded := &errs.Coded{Code: failure.result.Code, Exit: errs.ExitRuntime,
		Message: "selected image acquisition failed (" + failure.result.Code + ")",
		Fix:     "Inspect image_acquisition and any retained output before retrying; existing files are never overwritten."}
	var original *errs.Coded
	if errors.As(failure.cause, &original) {
		coded.Exit, coded.Fix = original.Exit, original.Fix
	}
	switch coded.Code {
	case "workiq_account_invalid":
		coded.Exit = errs.ExitEnvironment
	case "interrupted":
		coded.Exit = errs.ExitRuntime
		coded.Message = "selected image acquisition interrupted; inspect the receipt for any retained or published file"
		coded.Fix = "Inspect image_acquisition and retained files before retrying; nothing is overwritten."
	case "image_invalid_bytes", "image_output_failed", "image_output_uncertain", "image_native_close_failed",
		"image_output_cleanup_failed", "image_output_cleanup_ownership_unknown", "image_receipt_failed":
		coded.Exit = errs.ExitRuntime
	}
	doc := errorDoc{Error: bodyOf(coded), ImageAcquisition: &failure.result}
	encoder := json.NewEncoder(rt.stderr)
	encoder.SetEscapeHTML(false)
	if rt.format == output.Text {
		if _, err := fmt.Fprintf(rt.stderr, "error: %s\nfix: %s\n", coded.Message, coded.Fix); err != nil {
			return errs.ExitRuntime
		}
	}
	if err := encoder.Encode(doc); err != nil {
		return errs.ExitRuntime
	}
	return coded.Exit
}
