package teamsdesktop

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/botcitations"
	"github.com/ourostack/m365crawl/internal/strictjson"
)

type ApplicationProjection struct {
	State, FailureCode string
	Observation        botcitations.Observation
	Losses             []botcitations.Loss
	SourceSlots        *int
}

func ProjectApplication(ctx context.Context, messageID, senderID string, raw []byte, rawPresent bool) (result ApplicationProjection, err error) {
	if ctx == nil {
		return applicationFailure("teams_app_raw_invalid"), nil
	}
	defer func() {
		if contextErr := ctx.Err(); contextErr != nil {
			result, err = ApplicationProjection{}, contextErr
		}
	}()
	if !rawPresent {
		return applicationFailure("teams_app_raw_missing"), nil
	}
	if len(raw) > 8<<20 {
		return applicationFailure("teams_app_raw_too_large"), nil
	}
	budget := strictjson.Budget{Context: ctx, MaxDepth: 64, MaxMembers: 131072}
	value, parseErr := budget.Parse(raw)
	if errors.Is(parseErr, strictjson.ErrTooLarge) {
		return applicationFailure("teams_app_raw_too_large"), nil
	}
	message, object := value.(map[string]any)
	if parseErr != nil || !object {
		return applicationFailure("teams_app_raw_invalid"), nil
	}
	if decodeErr := decodeStringBotMetadata(&budget, message); errors.Is(decodeErr, strictjson.ErrTooLarge) {
		return applicationFailure("teams_app_raw_too_large"), nil
	} else if decodeErr != nil {
		return ApplicationProjection{}, decodeErr
	}
	if messageID == "" || !utf8.ValidString(messageID) {
		return applicationFailure("teams_app_identity_unqualified"), nil
	}
	slots := applicationSourceSlots(message)
	if !possibleApplicationClaims(message) {
		return ApplicationProjection{State: "absent", SourceSlots: slots}, nil
	}
	if senderID == "" || !utf8.ValidString(senderID) {
		return applicationFailure("teams_app_identity_unqualified"), nil
	}
	mapped, mapErr := botcitations.MapMessage(ctx, messageID, senderID, message)
	if mapErr != nil {
		var fixed *botcitations.MapError
		if errors.As(mapErr, &fixed) {
			return applicationFailure(fixed.Code), nil
		}
		return ApplicationProjection{}, mapErr
	}
	state := "mapped"
	if len(mapped.Losses) > 0 {
		state = "partial"
	}
	return ApplicationProjection{State: state, Observation: mapped.Observation, Losses: mapped.Losses, SourceSlots: slots}, nil
}

func applicationFailure(code string) ApplicationProjection {
	return ApplicationProjection{State: "failed", FailureCode: code}
}

func applicationSourceSlots(message map[string]any) *int {
	properties, _ := message["properties"].(map[string]any)
	if citations, ok := properties["botCitations"].([]any); ok {
		count := len(citations)
		return &count
	}
	return nil
}

func possibleApplicationClaims(message map[string]any) bool {
	value := message["properties"]
	if value == nil {
		return false
	}
	properties, object := value.(map[string]any)
	if !object {
		return true
	}
	for _, selected := range []struct {
		name   string
		fields []string
	}{{"fromAppMetadata", []string{"id", "name"}}, {"botMetadata", []string{"replyToId"}}} {
		value := properties[selected.name]
		if value == nil {
			continue
		}
		metadata, object := value.(map[string]any)
		if !object {
			return true
		}
		for _, field := range selected.fields {
			if metadata[field] != nil {
				return true
			}
		}
	}
	value = properties["botCitations"]
	if value == nil {
		return false
	}
	citations, array := value.([]any)
	return !array || len(citations) > 0
}

// decodeStringBotMetadata replaces a JSON-string properties.botMetadata, the container's native
// encoding, with the one object it holds, decoded within the record's own budget from the
// container's depth (root, properties, botMetadata). A string that does not hold exactly one valid
// object stays in place, so the mapper reports it as unmapped; only exhaustion and cancellation are
// errors. No other string property is reinterpreted.
func decodeStringBotMetadata(budget *strictjson.Budget, message map[string]any) error {
	properties, _ := message["properties"].(map[string]any)
	encoded, isString := properties["botMetadata"].(string)
	if !isString {
		return nil
	}
	value, err := budget.ParseAt([]byte(encoded), 3)
	if errors.Is(err, strictjson.ErrInvalid) {
		return nil
	}
	if err != nil {
		return err
	}
	if object, ok := value.(map[string]any); ok {
		properties["botMetadata"] = object
	}
	return nil
}
