package cli

import "testing"

func TestMailConversationSerializationInRelatedMessages(t *testing.T) {
	e, _ := prepEnv(t)
	id := planningID(t, e, "teams")
	doc := eventDoc(t, e, id, "--fields", "related_mail")
	first := doc["related_mail"].([]any)[0].(map[string]any)
	if v, ok := first["conversation_id"]; !ok || v != nil {
		t.Fatalf("legacy related-mail conversation must be null: %v", first)
	}
	e.exec(`update mail_messages set conversation_id='synthetic-related-conversation' where detail_key=9001`)
	doc = eventDoc(t, e, id, "--fields", "related_mail")
	first = doc["related_mail"].([]any)[0].(map[string]any)
	if first["conversation_id"] != "synthetic-related-conversation" || first["id"] != "outlook/Main:9001" {
		t.Fatalf("related-mail conversation/ID = %v", first)
	}
}
