package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/teamsimage"
)

const imagesHelp = "content images accepts one exact Teams message link, or a conversation ID and message ID. It is always archive-only, regardless of --max-age, and needs no WorkIQ feature or sign-in. --account selects an archived Teams tenant/user; otherwise exactly one archived account is required. Cached HTML img elements only: no CSS, attachments, OCR, image bytes or server-complete media. Missing HTML is unknown, not zero. Partial inspection cannot authorize selection. Bounds: 1MiB HTML, 64KiB token, 4096 img elements, 64 attributes, 1024-byte ID and 8192-byte source URL. Source URLs are never returned.\n"

type contentImagesCmd struct {
	Target    string `arg:"" help:"Exact Teams message link, or exact conversation ID."`
	MessageID string `arg:"" optional:"" help:"Exact message ID when TARGET is a conversation ID; forbidden with a message link."`
}

func (contentImagesCmd) Help() string { return imagesHelp }

type imageInventoryItem struct {
	Account                   string                     `json:"account"`
	ConversationID            string                     `json:"conversation_id"`
	MessageID                 string                     `json:"message_id"`
	Scope                     string                     `json:"scope"`
	InspectionState           teamsimage.InspectionState `json:"inspection_state"`
	ImageReferences           []teamsimage.Reference     `json:"image_references"`
	DecorativeEmojiCount      int                        `json:"decorative_emoji_count"`
	UnsupportedReferenceCount int                        `json:"unsupported_reference_count"`
	BinaryMedia               string                     `json:"binary_media"`
	ReasonCodes               []string                   `json:"reason_codes"`
}

type imageInventoryResult struct {
	item imageInventoryItem
	keys []string
	meta
}

func (r *imageInventoryResult) MarshalJSON() ([]byte, error) {
	var item any = r.item
	if len(r.keys) > 0 {
		var err error
		item, err = project(r.item, r.keys)
		if err != nil {
			return nil, err
		}
	}
	return joinJSON(item, r.meta)
}

func (r *imageInventoryResult) renderImages(rt *runtime) error {
	body, err := r.MarshalJSON()
	if err != nil {
		return err
	}
	var text bytes.Buffer
	if err := json.Indent(&text, body, "", "  "); err != nil {
		return err
	}
	_, err = io.WriteString(rt.stdout, text.String()+"\n")
	return err
}

func (c *contentImagesCmd) selector(rt *runtime) (string, string, error) {
	if rt.g.OutlookAccount != "" || rt.g.OutlookProfile != "" || rt.mailAccount() != "" {
		return "", "", errs.Usage("content images accepts only a Teams account, not Outlook selectors")
	}
	conversation, message := c.Target, c.MessageID
	if isLink(c.Target) {
		if message != "" {
			return "", "", errs.Usage("pass a message link alone, not a link plus a message ID")
		}
		var err *linkError
		conversation, message, _, err = parseTeamsLink(c.Target)
		if err != nil || message == "" {
			return "", "", errs.Usage("content images needs an exact Teams message link or conversation ID plus message ID")
		}
	}
	if !teamsimage.ValidComponent(conversation) || !teamsimage.ValidComponent(message) {
		return "", "", errs.Usage("content images needs bounded exact conversation/message IDs without controls or delimiters")
	}
	return conversation, message, nil
}

func imageAccount(rt *runtime, st *store.Store) (teamsdesktop.Account, error) {
	if rt.account != nil {
		if !teamsimage.ValidComponent(rt.account.TenantID) || !teamsimage.ValidComponent(rt.account.UserID) {
			return teamsdesktop.Account{}, errs.Usage("content images needs a bounded exact Teams tenant/user account")
		}
		ok, err := st.HasAccount(rt.ctx, *rt.account)
		if err != nil {
			return teamsdesktop.Account{}, err
		}
		if !ok {
			return teamsdesktop.Account{}, imageInventoryError("image_message_unknown")
		}
		return *rt.account, nil
	}
	accounts, err := st.TeamsAccounts(rt.ctx)
	if err != nil {
		return teamsdesktop.Account{}, err
	}
	if len(accounts) != 1 {
		return teamsdesktop.Account{}, errs.Usage("content images requires --account when the archive does not have exactly one Teams account")
	}
	a := accounts[0]
	if !teamsimage.ValidComponent(a.TenantID) || !teamsimage.ValidComponent(a.UserID) {
		return teamsdesktop.Account{}, errors.New("invalid archived Teams account identity")
	}
	return a, nil
}

func (c *contentImagesCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "content images", ""); err != nil {
		return err
	}
	conversation, message, err := c.selector(rt)
	if err != nil {
		return err
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		return imageInventoryError("image_message_unknown")
	}
	if err != nil {
		return asCoded(err)
	}
	res, readErr := c.inventory(rt, st, conversation, message)
	if err := errors.Join(readErr, st.Close()); err != nil {
		return asCoded(err)
	}
	if err := rt.write("content images", res); err != nil {
		return err
	}
	if res.item.InspectionState != teamsimage.InspectionComplete {
		code := "image_inspection_incomplete"
		if len(res.item.ReasonCodes) > 0 && res.item.ReasonCodes[0] == "image_body_unavailable" {
			code = "image_body_unavailable"
		}
		return imageInventoryError(code)
	}
	return nil
}

func (c *contentImagesCmd) inventory(rt *runtime, st *store.Store, conversation, message string) (*imageInventoryResult, error) {
	a, err := imageAccount(rt, st)
	if err != nil {
		return nil, err
	}
	key := store.MessageKey{TenantID: a.TenantID, UserID: a.UserID, ConversationID: conversation, ID: message}
	rows, err := st.MessagesByKey(rt.ctx, []string{strings.Join([]string{key.TenantID, key.UserID, key.ConversationID, key.ID}, "|")})
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, imageInventoryError("image_message_unknown")
	}
	bodies, err := st.MessageHTML(rt.ctx, []store.MessageKey{key})
	if err != nil {
		return nil, err
	}
	inspection, err := teamsimage.InspectHTML(rt.ctx, bodies[key])
	if err != nil {
		return nil, err
	}
	res := &imageInventoryResult{
		item: imageInventoryItem{
			Account: a.TenantID + "/" + a.UserID, ConversationID: conversation, MessageID: message,
			Scope: "selected_cached_message", InspectionState: inspection.State,
			ImageReferences: inspection.References, DecorativeEmojiCount: inspection.DecorativeEmojiCount,
			UnsupportedReferenceCount: inspection.UnsupportedReferenceCount,
			BinaryMedia:               "not_acquired_by_this_command", ReasonCodes: inspection.ReasonCodes,
		},
		keys: rt.fields, meta: meta{ImplicitSync: implicitSyncDisabled},
	}
	last, err := st.LastSuccessFor(rt.ctx, res.item.Account)
	if err != nil {
		return nil, err
	}
	if !last.IsZero() {
		age := max(int64(rt.now().Sub(last).Seconds()), 0)
		res.ArchiveAgeSeconds = &age
	}
	return res, nil
}

func imageInventoryError(code string) error {
	return &errs.Coded{Code: code, Exit: errs.ExitEnvironment,
		Message: fmt.Sprintf("selected cached image inventory is unavailable or incomplete (%s)", code),
		Fix:     "Check the exact Teams account/message and its cached HTML; do not interpret unavailable content as zero images."}
}
