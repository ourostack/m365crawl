package calendar

import (
	"fmt"
	"testing"
)

func TestAllUnknownFieldsReturnsIndependentCompleteVocabulary(t *testing.T) {
	const expected = "[attachments attendees body body_preview categories dial_in join_url location meeting_chat_id organizer organizer_address recurrence reminder response rooms short_join_url show_as subject time_zone time_zone_iana utc_offset]"
	fields := AllUnknownFields()
	if fmt.Sprint(fields) != expected {
		t.Fatal("complete unknown vocabulary differs from the declared non-flag contract")
	}
	fields[0] = Field("synthetic-mutated-copy")
	if fmt.Sprint(AllUnknownFields()) != expected {
		t.Fatal("caller mutated the shared unknown vocabulary")
	}
}
