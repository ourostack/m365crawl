package cli

import (
	"fmt"
	"io"
)

type mediaCoverage struct {
	ReturnedMessages    int    `json:"returned_messages"`
	Scope               string `json:"scope"`
	BinaryMedia         string `json:"binary_media"`
	ReferenceInspection string `json:"reference_inspection"`
}

func mediaCoverageForReturnedMessages(count int) *mediaCoverage {
	return &mediaCoverage{ReturnedMessages: count, Scope: "returned_teams_messages",
		BinaryMedia: "not_included", ReferenceInspection: "not_requested"}
}

func renderMediaCoverage(w io.Writer, coverage *mediaCoverage) {
	if coverage != nil {
		_, _ = fmt.Fprintf(w, "media bytes not included for %d returned Teams messages; reference inspection not requested\n",
			coverage.ReturnedMessages)
	}
}

const messageMediaHelp = "Message-bearing results carry media_coverage outside --fields: returned_messages counts only returned Teams messages (after the merged search limit); binary_media is not_included and reference_inspection is not_requested, even for zero messages. Text/HTML and application claims do not contain image bytes or prove complete media. Inspect one exact cached message with content images; explicitly content fetch image for one supported PNG. Calendar scope is only returned recent chat messages; each watch message event counts one, while activity/sync/error events and conversation/unread summaries, SQL and records gain no guessed media scope.\n"
