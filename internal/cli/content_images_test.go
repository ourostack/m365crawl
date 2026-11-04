package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

const imageConversation = "19:synthetic-image@thread.v2"
const imageMessage = "synthetic-message"
const imageElement = `<img itemtype="http://schema.skype.com/AMSImage" itemid="synthetic-image" src="https://eu-api.asm.skype.com/v1/objects/synthetic-image/views/imgpsh_fullsize">`

func imageInventoryEnv(t *testing.T, body string) *env {
	t.Helper()
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
	_, err = st.ApplyMessages(ctx, []teamsdesktop.Message{{
		TenantID: tenantA, UserID: userA, ConversationID: imageConversation, ID: imageMessage,
		MessageType: "Text", ContentType: "html", ContentHTML: body,
	}})
	if closeErr := st.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestImageInventoryContentAndEmoji(t *testing.T) {
	const body = `<p>Generated illustration</p>` +
		`<img itemtype="http://schema.skype.com/AMSImage" itemid="synthetic-image" class="Emoji" width="7" height="3" src="https://eu-api.asm.skype.com/v1/objects/synthetic-image/views/imgpsh_fullsize">` +
		`<img itemtype="http://schema.skype.com/Emoji" class="Emoji" src="https://example.test/smile.png">` +
		`<img itemtype="http://schema.skype.com/Emoji" class="Emoji" src="https://example.test/wave.png">`
	e := imageInventoryEnv(t, body)
	code, out, stderr := e.run("--json", "--max-age", "1", "content", "images", imageConversation, imageMessage)
	if code != 0 {
		t.Fatalf("offline selected-message image inventory exit %d: %s", code, stderr)
	}
	doc := decode(t, out)
	if doc["scope"] != "selected_cached_message" || doc["inspection_state"] != "complete" ||
		doc["account"] != tenantA+"/"+userA || doc["conversation_id"] != imageConversation ||
		doc["message_id"] != imageMessage || doc["decorative_emoji_count"] != float64(2) ||
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

func TestImageInventoryLimits(t *testing.T) {
	withAttributes := func(extra int) string {
		var b strings.Builder
		b.WriteString(strings.TrimSuffix(imageElement, ">"))
		for i := range extra {
			fmt.Fprintf(&b, ` data-x%d=""`, i)
		}
		b.WriteByte('>')
		return b.String()
	}
	urlPrefix := "https://eu-api.asm.skype.com/v1/objects/synthetic-image/views/imgpsh_fullsize?ticket="
	withURL := func(size int) string {
		return strings.Replace(imageElement,
			"https://eu-api.asm.skype.com/v1/objects/synthetic-image/views/imgpsh_fullsize",
			urlPrefix+strings.Repeat("x", size-len(urlPrefix)), 1)
	}
	for _, tc := range []struct {
		name, body string
		incomplete bool
		unsupported int
	}{
		{"body-exact", strings.Repeat("<p>x</p>", 131072), false, 0},
		{"body-over", strings.Repeat("<p>x</p>", 131072) + "x", true, 0},
		{"token-exact", strings.Repeat("x", 65536), false, 0},
		{"token-over", strings.Repeat("x", 65537), true, 0},
		{"images-exact", strings.Repeat(imageElement, 4096), false, 0},
		{"images-over", strings.Repeat(imageElement, 4097), true, 0},
		{"attributes-exact", withAttributes(61), false, 0},
		{"attributes-over", withAttributes(62), true, 0},
		{"id-exact", strings.Replace(imageElement, `itemid="synthetic-image"`, `itemid="`+strings.Repeat("z", 1024)+`"`, 1), false, 0},
		{"id-over", strings.Replace(imageElement, `itemid="synthetic-image"`, `itemid="`+strings.Repeat("z", 1025)+`"`, 1), false, 1},
		{"url-exact", withURL(8192), false, 0},
		{"url-over", withURL(8193), false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := imageInventoryEnv(t, tc.body)
			code, out, stderr := e.run("--json", "content", "images", imageConversation, imageMessage)
			if tc.incomplete {
				if code != 3 || errorCode(t, stderr) != "image_inspection_incomplete" {
					t.Fatalf("limit did not produce a coded incomplete inventory: %d %s %s", code, out, stderr)
				}
				if decode(t, out)["inspection_state"] == "complete" {
					t.Fatal("exhausted inspection claimed complete")
				}
			} else {
				if code != 0 {
					t.Fatalf("bounded or individually unsupported reference failed: %d %s", code, stderr)
				}
				doc := decode(t, out)
				if doc["inspection_state"] != "complete" || doc["unsupported_reference_count"] != float64(tc.unsupported) {
					t.Fatalf("wrong bounded inspection: %s", out)
				}
			}
		})
	}
}

func TestImageSelectionRefusesPartialPrefix(t *testing.T) {
	e := imageInventoryEnv(t, imageElement+strings.Repeat("x", 65537)+imageElement)
	code, out, stderr := e.run("--json", "content", "images", imageConversation, imageMessage)
	if code != 3 || errorCode(t, stderr) != "image_inspection_incomplete" {
		t.Fatalf("partial selected prefix was not refused: %d %s %s", code, out, stderr)
	}
	doc := decode(t, out)
	if doc["inspection_state"] != "partial" {
		t.Fatalf("observed prefix lost its partial scope: %s", out)
	}
	refs, ok := doc["image_references"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("observed prefix was not retained: %s", out)
	}
	ref := refs[0].(map[string]any)
	selection, ok := ref["selection"].(string)
	if !ok || selection == "" || selection == "supported" {
		t.Fatalf("partial prefix authorized selection: %s", out)
	}
}

func TestImageSelectorExactMessageAndAccount(t *testing.T) {
	e := imageInventoryEnv(t, imageElement)
	link := "https://teams.microsoft.com/l/message/19%3Asynthetic-image%40thread.v2/synthetic-message?tenantId=" + tenantA + "&parentMessageId=not-the-selected-message"
	code, out, stderr := e.run("--json", "content", "images", link)
	if code != 0 || decode(t, out)["message_id"] != imageMessage {
		t.Fatalf("link's exact message was substituted: %d %s %s", code, out, stderr)
	}
	for _, args := range [][]string{
		{link, imageMessage},
		{imageConversation},
		{imageConversation + "|extra", imageMessage},
		{imageConversation, "bad\nid"},
	} {
		code, _, stderr := e.run(append([]string{"--json", "content", "images"}, args...)...)
		if code != 2 {
			t.Fatalf("invalid selector accepted: %v, %d %s", args, code, stderr)
		}
	}
	code, _, stderr = e.run("--json", "--outlook-account", "Main", "content", "images", imageConversation, imageMessage)
	if code != 2 {
		t.Fatalf("Outlook selector accepted: %d %s", code, stderr)
	}
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	err = st.ApplyAccount(context.Background(), teamsdesktop.Account{TenantID: tenantA, UserID: "00000000-0000-4000-8000-0000000000b1"})
	if closeErr := st.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr = e.run("--json", "content", "images", imageConversation, imageMessage)
	if code != 2 {
		t.Fatalf("ambiguous account selected implicitly: %d %s", code, stderr)
	}
	code, out, stderr = e.run("--json", "--account", tenantA+"/"+userA, "content", "images", imageConversation, imageMessage)
	if code != 0 || decode(t, out)["account"] != tenantA+"/"+userA {
		t.Fatalf("exact archived account not accepted: %d %s %s", code, out, stderr)
	}
}

func TestImageOfflineWithoutFeatureTables(t *testing.T) {
	e := imageInventoryEnv(t, imageElement)
	e.exec("drop table workiq_accounts")
	code, out, stderr := e.run("--json", "--max-age", "1", "content", "images", imageConversation, imageMessage)
	if code != 0 || decode(t, out)["implicit_sync"] != "disabled" {
		t.Fatalf("offline inventory coupled to native feature tables or implicit sync: %d %s %s", code, out, stderr)
	}
}

func TestImageInventoryMissingIsNotZero(t *testing.T) {
	e := imageInventoryEnv(t, "")
	code, out, stderr := e.run("--json", "content", "images", imageConversation, "absent-message")
	if code != 3 || errorCode(t, stderr) != "image_message_unknown" || out != "" {
		t.Fatalf("absent exact message fabricated an inventory: %d %s %s", code, out, stderr)
	}
	code, out, stderr = e.run("--json", "content", "images", imageConversation, imageMessage)
	if code != 3 || errorCode(t, stderr) != "image_body_unavailable" || decode(t, out)["inspection_state"] != "unknown" {
		t.Fatalf("missing body became zero references: %d %s %s", code, out, stderr)
	}
	e = imageInventoryEnv(t, "<p>Only generated text.</p>")
	code, out, stderr = e.run("--json", "content", "images", imageConversation, imageMessage)
	if code != 0 {
		t.Fatal(code, stderr)
	}
	doc := decode(t, out)
	refs, ok := doc["image_references"].([]any)
	if doc["inspection_state"] != "complete" || !ok || len(refs) != 0 {
		t.Fatalf("inspected zero references was not represented truthfully: %s", out)
	}
}
