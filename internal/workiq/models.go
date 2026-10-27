// Package workiq owns the explicit, credential-free WorkIQ subprocess transport.
package workiq

import "encoding/json"

// Invocation carries explicit source-root selection to the sole spawn guard.
type Invocation struct {
	ExplicitTeamsRoot   bool
	ExplicitOutlookRoot bool
}

// Page contains only a validated, scrubbed server result.
type Page struct {
	JSON        json.RawMessage
	Data        json.RawMessage
	StatusCode  int
	NextCursor  string
	DeltaCursor string
	RetryAfter  string
}
