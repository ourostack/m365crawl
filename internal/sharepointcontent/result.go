package sharepointcontent

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/transcripts"
)

var nativeUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func decode(ctx context.Context, request admittedRequest, raw scriptResult) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if request.Kind != "page" && request.Kind != "stream" || raw.HTTPStatus != 0 && (raw.HTTPStatus < 100 || raw.HTTPStatus > 599) {
		return Result{}, &ReadError{Code: "malformed"}
	}
	if raw.State == "no_access" && raw.HTTPStatus != 403 || raw.State == "not_found" && raw.HTTPStatus != 404 {
		return Result{}, &ReadError{Code: "malformed"}
	}
	requiresTranscriptIdentity := false
	switch raw.State {
	case "transcript_observations", "no_transcript", "transcript_selection_required", "collection_incomplete", "unsupported_download_host":
		if request.Kind != "stream" {
			return Result{}, &ReadError{Code: "malformed"}
		}
		requiresTranscriptIdentity = true
	}
	switch raw.State {
	case "no_access", "not_found", "timeout":
		if raw.File.FileID == "" {
			return Result{}, &ReadError{Code: raw.State, HTTPStatus: raw.HTTPStatus}
		}
	case "signin_required", "elsewhere", "failed", "malformed", "identity_mismatch", "too_large", "unexpected_response":
		return Result{}, &ReadError{Code: raw.State, HTTPStatus: raw.HTTPStatus}
	case "metadata_only", "page_observations", "transcript_observations", "no_transcript", "transcript_selection_required", "collection_incomplete", "unsupported_download_host":
	default:
		return Result{}, &ReadError{Code: "malformed"}
	}
	if (raw.HTTPStatus != 200 && raw.State != "no_access" && raw.State != "not_found" && raw.State != "timeout") || !nativeUUID.MatchString(raw.File.SiteID) || !nativeUUID.MatchString(raw.File.WebID) ||
		!nativeUUID.MatchString(raw.File.FileID) || raw.Account.ID < 1 || raw.Account.ID > 1<<53-1 || raw.Account.LoginName == "" {
		return Result{}, &ReadError{Code: "malformed"}
	}
	if raw.Account.Host != request.Host || raw.Account.WebID != raw.File.WebID || raw.File.Path != request.Path {
		return Result{}, &ReadError{Code: "identity_mismatch"}
	}
	if requiresTranscriptIdentity && (raw.File.DriveID == nil || *raw.File.DriveID == "" || raw.File.ItemID == nil || *raw.File.ItemID == "") {
		return Result{}, &ReadError{Code: "malformed"}
	}
	var retained int
	stringsToCheck := []*string{&raw.Account.Host, &raw.Account.WebID, &raw.Account.LoginName, &raw.File.SiteID, &raw.File.WebID,
		&raw.File.FileID, &raw.File.Path, raw.File.DriveID, raw.File.ItemID, raw.File.Title, raw.File.ModifiedRaw}
	for _, value := range stringsToCheck {
		if value == nil {
			continue
		}
		if len(*value) > 256<<10 {
			return Result{}, &ReadError{Code: "too_large"}
		}
		if !utf8.ValidString(*value) || strings.ContainsRune(*value, 0) {
			return Result{}, &ReadError{Code: "malformed"}
		}
		retained += len(*value)
	}
	if (raw.File.ListItemID != nil && (*raw.File.ListItemID < 1 || *raw.File.ListItemID > 1<<53-1)) ||
		(raw.File.Length != nil && (*raw.File.Length < 0 || *raw.File.Length > 1<<53-1)) {
		return Result{}, &ReadError{Code: "malformed"}
	}
	losses := make([]Loss, 0, 1)
	seenLosses := map[string]bool{}
	for _, loss := range raw.Losses {
		switch loss.Code {
		case "modified_time_unmapped", "control_data_unmapped", "control_id_unmapped", "control_text_unmapped", "rich_text_unavailable":
		default:
			return Result{}, &ReadError{Code: "malformed"}
		}
		if loss.Count < 1 || loss.Count > 4096 || seenLosses[loss.Code] || (loss.Code == "modified_time_unmapped" && loss.Count != 1) {
			return Result{}, &ReadError{Code: "malformed"}
		}
		seenLosses[loss.Code] = true
		losses = append(losses, loss)
	}
	raw.File.ModifiedAt = nil
	if raw.File.ModifiedRaw != nil {
		if parsed, err := time.Parse(time.RFC3339Nano, *raw.File.ModifiedRaw); err == nil {
			raw.File.ModifiedAt = &parsed
		} else if !seenLosses["modified_time_unmapped"] {
			losses = append(losses, Loss{Code: "modified_time_unmapped", Count: 1})
		}
	}
	controls := make([]PageControl, 0, len(raw.PageControls))
	if len(raw.PageControls) > 4096 {
		return Result{}, &ReadError{Code: "too_large"}
	}
	if (raw.State != "page_observations" && len(raw.PageControls) != 0) || (raw.State == "page_observations" && request.Kind != "page") {
		return Result{}, &ReadError{Code: "malformed"}
	}
	textBytes := 0
	textObservations := 0
	for i, control := range raw.PageControls {
		textObservations += len(control.Texts)
		if textObservations > 65536 {
			return Result{}, &ReadError{Code: "too_large"}
		}
		if control.Ordinal != i || (control.ID != nil && !nativeUUID.MatchString(*control.ID)) {
			return Result{}, &ReadError{Code: "malformed"}
		}
		switch control.State {
		case "text":
			if control.Type == nil || *control.Type != 4 || len(control.Texts) == 0 {
				return Result{}, &ReadError{Code: "malformed"}
			}
		case "layout":
			if control.Type == nil || *control.Type != 0 || len(control.Texts) != 0 {
				return Result{}, &ReadError{Code: "malformed"}
			}
		case "omitted":
			if control.Type == nil || *control.Type == 0 || *control.Type == 4 || len(control.Texts) != 0 {
				return Result{}, &ReadError{Code: "malformed"}
			}
		case "unmapped":
			if len(control.Texts) != 0 {
				return Result{}, &ReadError{Code: "malformed"}
			}
		default:
			return Result{}, &ReadError{Code: "malformed"}
		}
		texts := make([]string, len(control.Texts))
		for j, text := range control.Texts {
			if len(text) > 256<<10 {
				return Result{}, &ReadError{Code: "too_large"}
			}
			if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
				return Result{}, &ReadError{Code: "malformed"}
			}
			textBytes += len(text)
			retained += len(text)
			if textBytes > 1<<20 || retained > 8<<20 {
				return Result{}, &ReadError{Code: "too_large"}
			}
			texts[j] = text
		}
		if control.ID != nil {
			id := *control.ID
			control.ID = &id
		}
		if control.Type != nil {
			kind := *control.Type
			control.Type = &kind
		}
		control.Texts = texts
		controls = append(controls, control)
	}
	for _, field := range []*(*string){&raw.File.DriveID, &raw.File.ItemID, &raw.File.Title, &raw.File.ModifiedRaw} {
		if *field != nil {
			value := **field
			*field = &value
		}
	}
	if raw.File.ListItemID != nil {
		value := *raw.File.ListItemID
		raw.File.ListItemID = &value
	}
	if raw.File.Length != nil {
		value := *raw.File.Length
		raw.File.Length = &value
	}
	var transcript *Transcript
	if raw.State == "transcript_observations" {
		if request.Kind != "stream" || raw.Transcript == nil || raw.Transcript.ID == "" {
			return Result{}, &ReadError{Code: "malformed"}
		}
		if len(raw.Transcript.Entries) > 16384 {
			return Result{}, &ReadError{Code: "too_large"}
		}
		if request.TranscriptID != "" && raw.Transcript.ID != request.TranscriptID {
			return Result{}, &ReadError{Code: "identity_mismatch"}
		}
		if len(raw.Transcript.ID) > 256<<10 {
			return Result{}, &ReadError{Code: "too_large"}
		}
		if !utf8.ValidString(raw.Transcript.ID) || strings.ContainsRune(raw.Transcript.ID, 0) {
			return Result{}, &ReadError{Code: "malformed"}
		}
		retained += len(raw.Transcript.ID)
		transcript = &Transcript{ID: raw.Transcript.ID, Entries: make([]TranscriptEntry, 0, len(raw.Transcript.Entries))}
		for i, entry := range raw.Transcript.Entries {
			if entry.Ordinal != i || entry.ID == "" {
				return Result{}, &ReadError{Code: "malformed"}
			}
			for _, value := range []*string{&entry.ID, &entry.Text, entry.SpeakerDisplayName, entry.StartRaw, entry.EndRaw} {
				if value == nil {
					continue
				}
				if len(*value) > 256<<10 {
					return Result{}, &ReadError{Code: "too_large"}
				}
				if !utf8.ValidString(*value) || strings.ContainsRune(*value, 0) {
					return Result{}, &ReadError{Code: "malformed"}
				}
				retained += len(*value)
				if retained > 8<<20 {
					return Result{}, &ReadError{Code: "too_large"}
				}
			}
			textBytes += len(entry.Text)
			if textBytes > 1<<20 {
				return Result{}, &ReadError{Code: "too_large"}
			}
			entry.StartMS, entry.EndMS = nil, nil
			for _, field := range []struct {
				raw    **string
				parsed **int64
			}{{&entry.StartRaw, &entry.StartMS}, {&entry.EndRaw, &entry.EndMS}} {
				if *field.raw != nil {
					value := **field.raw
					*field.raw = &value
					if safeClock.MatchString(value) {
						decoded, bad := transcripts.DecodeEntries([]transcripts.RawEntry{{B: value, E: value}})
						if bad == 0 {
							*field.parsed = decoded[0].StartMS
							continue
						}
					}
				}
				addLoss(&losses, "transcript_offset_unmapped")
			}
			if entry.StartMS != nil && entry.EndMS != nil && *entry.EndMS < *entry.StartMS {
				entry.StartMS, entry.EndMS = nil, nil
				addLoss(&losses, "transcript_offset_reversed")
			}
			if entry.SpeakerDisplayName != nil {
				value := *entry.SpeakerDisplayName
				entry.SpeakerDisplayName = &value
			}
			transcript.Entries = append(transcript.Entries, entry)
		}
	} else if raw.Transcript != nil {
		return Result{}, &ReadError{Code: "malformed"}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	slices.SortFunc(losses, func(a, b Loss) int { return strings.Compare(a.Code, b.Code) })
	return Result{Kind: request.Kind, State: raw.State, HTTPStatus: raw.HTTPStatus, SourceAccount: raw.Account,
		File: raw.File, Partial: len(losses) > 0, Losses: losses, PageControls: controls, Transcript: transcript}, nil
}

var safeClock = regexp.MustCompile(`^[0-9]{1,6}:[0-5][0-9]:[0-5][0-9](\.[0-9]{1,7})?$`)

func addLoss(losses *[]Loss, code string) {
	for i := range *losses {
		if (*losses)[i].Code == code {
			(*losses)[i].Count++
			return
		}
	}
	*losses = append(*losses, Loss{Code: code, Count: 1})
}
