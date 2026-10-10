package engagecontent

import (
	"encoding/json"
	"time"
)

type ReadError struct{ Code string }

func (e *ReadError) Error() string { return "engage_content: " + e.Code }

type SourceAccount struct{ Host, NetworkID, UserID string }

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	State, Qualification string
	Complete             bool
	Account              SourceAccount
	Threads              []ThreadObservation
	Losses               []Loss
}

type ThreadObservation struct {
	Ordinal                              int
	ID, NetworkID, GroupID, StarterID    string
	CreatedRaw, UpdatedRaw               *string
	StarterCreatedRaw, StarterUpdatedRaw *string
	CreatedAt, UpdatedAt                 *time.Time
	StarterCreatedAt, StarterUpdatedAt   *time.Time
	SenderID, Language, Title            *string
	Version                              *int64
	IsDeleted, IsDraft                   *bool
	Blocks                               []string
}

type scriptResult struct {
	State, Fatal, Generation string
	Account                  SourceAccount
	AccountEvidence          []SourceAccount
	ViewerFragments          []SourceAccount
	Threads                  []scriptThread
	Losses                   []Loss
}

type scriptThread struct {
	Ordinal                              int
	ID, NetworkID, GroupID, StarterID    string
	CreatedRaw, UpdatedRaw               json.RawMessage
	StarterCreatedRaw, StarterUpdatedRaw json.RawMessage
	SenderID, Language, Title            json.RawMessage
	Version, IsDeleted, IsDraft          json.RawMessage
	Blocks                               []string
}
