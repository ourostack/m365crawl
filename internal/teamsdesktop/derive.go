package teamsdesktop

import (
	"encoding/json"
	"errors"

	"github.com/ourostack/teamscrawl/internal/v8"
)

// The Derive functions recompute, from a stored raw_json, the fields the mappers derive from it by
// interpretation: the readable message text, the sender name and the conversation display name.
// The store uses them to bring an archive made by an older build up to date without the Teams
// cache. They give the same answer as the mappers do on the live record, because both run the
// same code; the stored JSON is the canonical form of that record, so only values JSON cannot
// carry (dates, bytes) differ, and none of the derived fields read those.

// DeriveMessage returns the text and sender name MapReplyChain gives the message whose stored raw
// JSON is raw.
func DeriveMessage(raw []byte) (text, senderName string, err error) {
	mo, err := rawObject(raw)
	if err != nil {
		return "", "", err
	}
	text = messageText(str(fld(mo, "messageType")), str(fld(mo, "content")), fld(fld(mo, "properties"), "cards"))
	return text, firstNonEmpty(str(fld(mo, "imDisplayName")), str(fld(mo, "fromDisplayNameInToken"))), nil
}

// DeriveConversationName returns the display name MapConversation gives the conversation whose
// stored raw JSON is raw, for the account whose user id is userID.
func DeriveConversationName(userID string, raw []byte) (string, error) {
	co, err := rawObject(raw)
	if err != nil {
		return "", err
	}
	c, _, err := MapConversation(Account{UserID: userID}, co)
	if err != nil {
		return "", err
	}
	return c.DisplayName, nil
}

func rawObject(raw []byte) (*v8.Object, error) {
	var j any
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, err
	}
	o, ok := fromJSON(j).(*v8.Object)
	if !ok {
		return nil, errors.New("raw record is not an object")
	}
	return o, nil
}
