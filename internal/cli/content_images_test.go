package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestImageInventoryContentAndEmoji(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "must-not-start"))
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	a := teamsdesktop.Account{TenantID: tenantA, UserID: userA}
	if err := st.ApplyAccount(ctx, a); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	const conversation = "19:synthetic-image@thread.v2"
	const message = "synthetic-message"
	const body = `<p>Generated illustration</p>` +
		`<img itemtype="http://schema.skype.com/AMSImage" itemid="synthetic-image" class="Emoji" width="7" height="3" src="https://eu-api.asm.skype.com/v1/objects/synthetic-image/views/imgpsh_fullsize">` +
		`<img itemtype="http://schema.skype.com/Emoji" class="Emoji" src="https://example.test/smile.png">` +
		`<img itemtype="http://schema.skype.com/Emoji" class="Emoji" src="https://example.test/wave.png">`
	_, err = st.ApplyMessages(ctx, []teamsdesktop.Message{{
		TenantID: tenantA, UserID: userA, ConversationID: conversation, ID: message,
		MessageType: "Text", ContentType: "html", ContentHTML: body,
	}})
	if closeErr := st.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	code, out, stderr := e.run("--json", "--max-age", "1", "content", "images", conversation, message)
	if code != 0 {
		t.Fatalf("offline selected-message image inventory exit %d: %s", code, stderr)
	}
	doc := decode(t, out)
	if doc["scope"] != "selected_cached_message" || doc["inspection_state"] != "complete" ||
		doc["account"] != tenantA+"/"+userA || doc["conversation_id"] != conversation ||
		doc["message_id"] != message || doc["decorative_emoji_count"] != float64(2) ||
		doc["unsupported_reference_count"] != float64(0) ||
		doc["binary_media"] != "not_acquired_by_this_command" || doc["implicit_sync"] != "disabled" {
		t.Fatalf("wrong bounded reference coverage: %s", out)
	}
	refs, ok := doc["image_references"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("expected one content reference, not decorations: %s", out)
	}
	ref, ok := refs[0].(map[string]any)
	if !ok || ref["image_id"] != "synthetic-image" || ref["occurrence_count"] != float64(1) ||
		ref["width"] != float64(7) || ref["height"] != float64(3) {
		t.Fatalf("wrong exact image reference: %s", out)
	}
	if strings.Contains(out+stderr, "eu-api.asm.skype.com") || strings.Contains(out+stderr, "example.test") {
		t.Fatalf("source URL exposed by inventory: %s %s", out, stderr)
	}
}
