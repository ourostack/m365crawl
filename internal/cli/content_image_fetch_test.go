package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestImageFetchAdmissionBeforeNative(t *testing.T) {
	for _, tc := range []struct {
		name, body, message, image, code string
	}{
		{"missing-message", imageElement, "absent", "synthetic-image", "image_message_unknown"},
		{"missing-body", "", imageMessage, "synthetic-image", "image_body_unavailable"},
		{"partial-body", imageElement + strings.Repeat("x", 65537), imageMessage, "synthetic-image", "image_inspection_incomplete"},
		{"missing-reference", imageElement, imageMessage, "absent", "image_reference_unsupported"},
		{"ambiguous-reference", imageElement + strings.Replace(imageElement, "imgpsh_fullsize", "other-view", 1), imageMessage, "synthetic-image", "image_reference_ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := imageInventoryEnv(t, tc.body)
			dir := t.TempDir()
			output := filepath.Join(dir, "selected.png")
			code, out, stderr := e.run("--json", "content", "fetch", "image", imageConversation, tc.message,
				"--image-id", tc.image, "--output", output)
			if code != 3 || errorCode(t, stderr) != tc.code || out != "" {
				t.Fatalf("cached selection did not refuse before native acquisition: %d %s %s", code, out, stderr)
			}
			receipt, ok := decode(t, stderr)["image_acquisition"].(map[string]any)
			if !ok || receipt["status"] != "refused" || receipt["file_published"] != false ||
				receipt["output_verified"] != false || receipt["scope"] != "selected_image" {
				t.Fatalf("missing truthful refusal receipt: %s", stderr)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("cached refusal left output or scratch: %v %v", entries, err)
			}
		})
	}
}

func TestImageFetchNoArchiveMutation(t *testing.T) {
	e := imageInventoryEnv(t, imageElement)
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "existing.png")
	const original = "keep this exact existing file"
	if err := os.WriteFile(output, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := e.run("--json", "--max-age", "1m", "--account", tenantA+"/"+userA,
		"content", "fetch", "image", imageConversation, imageMessage, "--image-id", "synthetic-image", "--output", output)
	if code != 3 || errorCode(t, stderr) != "image_output_exists" || out != "" {
		t.Fatalf("existing output must refuse before optional-source admission: %d %s %s", code, out, stderr)
	}
	after, err := os.ReadFile(e.db)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("explicit image fetch mutated the archive: %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != original {
		t.Fatalf("explicit image fetch replaced existing output: %q %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("output refusal leaked a reservation: %v %v", entries, err)
	}
}

func TestImageFetchSelectorUsage(t *testing.T) {
	e := imageInventoryEnv(t, imageElement)
	output := filepath.Join(t.TempDir(), "selected.png")
	link := "https://teams.microsoft.com/l/message/19%3Asynthetic-image%40thread.v2/synthetic-message"
	for _, tail := range [][]string{
		{imageConversation},
		{link, imageMessage},
		{imageConversation, "bad\nmessage"},
	} {
		args := append([]string{"--json", "content", "fetch", "image"}, tail...)
		args = append(args, "--image-id", "synthetic-image", "--output", output)
		if code, _, stderr := e.run(args...); code != 2 {
			t.Fatalf("invalid exact selector accepted: %v: %d %s", tail, code, stderr)
		}
	}

	for _, flag := range []string{"--outlook-account", "--outlook-profile"} {
		code, _, stderr := e.run("--json", flag, "Main", "content", "fetch", "image",
			imageConversation, imageMessage, "--image-id", "synthetic-image", "--output", output)
		if code != 2 {
			t.Fatalf("Outlook selector accepted: %s: %d %s", flag, code, stderr)
		}
	}
}

func imageNativeEnv(t *testing.T, mode string) (*env, string) {
	t.Helper()
	e, _ := documentEnv(t)
	t.Setenv("WORKIQ_IMAGE_CASE", mode)
	transcript := os.Getenv("WORKIQ_ACQUISITION_TRANSCRIPT")
	t.Setenv("WORKIQ_DOCUMENT_TRANSCRIPT", transcript)
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ApplyMessages(context.Background(), []teamsdesktop.Message{{
		TenantID: "synthetic-tenant", UserID: "synthetic-user", ConversationID: "chat", ID: "image-message",
		MessageType: "Text", ContentType: "html", ContentHTML: imageElement,
	}})
	closeErr := st.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	return e, transcript
}

func imageFetchArgs(e *env, output string) []string {
	return []string{"--db", e.db, "--teams-root", e.root, "--json", "--max-age", "1m",
		"--account", "synthetic-tenant/synthetic-user", "content", "fetch", "image",
		"chat", "image-message", "--image-id", "synthetic-image", "--output", output}
}

func imageNativeCalls(t *testing.T, transcript string) []string {
	t.Helper()
	data, err := os.ReadFile(transcript)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var call struct{ Tool, Path string }
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call.Tool+":"+call.Path)
	}
	return calls
}

func TestFetchChatImageAdmissionOrder(t *testing.T) {
	for _, mode := range []string{"wrong-user", "invalid-address", "wrong-tenant", "wrong-chat", "channel", "unknown-kind", "deleted", "unsafe-reference"} {
		t.Run(mode, func(t *testing.T) {
			e, transcript := imageNativeEnv(t, mode)
			dir := t.TempDir()
			output := filepath.Join(dir, "selected.png")
			var out, stderr bytes.Buffer
			code := runCLI(context.Background(), imageFetchArgs(e, output), &out, &stderr)
			if code == 0 || code == 2 || out.Len() != 0 {
				t.Fatalf("native identity/refusal gate not reached: %d %s %s", code, &out, &stderr)
			}
			calls := imageNativeCalls(t, transcript)
			if len(calls) == 0 || calls[0] != "fetch:/me" || strings.Contains(strings.Join(calls, "\n"), "fetch_blob:") {
				t.Fatalf("unsafe binding acquired bytes or skipped native identity: %v", calls)
			}
			receipt, ok := decode(t, stderr.String())["image_acquisition"].(map[string]any)
			if !ok || receipt["file_published"] != false {
				t.Fatalf("native refusal lost publication effect: %s", &stderr)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("native refusal leaked reservation: %v %v", entries, err)
			}
			if strings.Contains(stderr.String(), "private-ticket") || strings.Contains(stderr.String(), "example.invalid") {
				t.Fatalf("native reference escaped to stderr: %s", &stderr)
			}
		})
	}
}

func TestFetchChatImageRecheckAndClose(t *testing.T) {
	for _, mode := range []string{"good", "clockless", "changed-reference", "changed-clock", "lost-clock", "deleted-after-blob", "invalid-bytes", "wrong-format", "blob-failure"} {
		t.Run(mode, func(t *testing.T) {
			e, transcript := imageNativeEnv(t, mode)
			before, err := os.ReadFile(e.db)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			output := filepath.Join(dir, "selected.png")
			var out, stderr bytes.Buffer
			code := runCLI(context.Background(), imageFetchArgs(e, output), &out, &stderr)
			after, err := os.ReadFile(e.db)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("acquisition changed archive bytes/cursors/config: %v", err)
			}
			calls := imageNativeCalls(t, transcript)
			if mode != "good" && mode != "clockless" {
				if code == 0 || code == 2 || out.Len() != 0 {
					t.Fatalf("native refusal was hidden: %d %s %s", code, &out, &stderr)
				}
				if len(calls) < 4 || calls[3] != "fetch_blob:/chats/chat/messages/image-message/hostedContents/selected-content/$value" {
					t.Fatalf("selected-only blob route was not exercised: %v", calls)
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 0 {
					t.Fatalf("refused recheck/bytes left output or scratch: %v %v", entries, err)
				}
				if strings.Contains(stderr.String(), "private-ticket") || strings.Contains(stderr.String(), "private native") {
					t.Fatal("native error or bytes escaped to stderr", stderr.String())
				}
				return
			}
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("selected PNG was not acquired: %d %s %s", code, &out, &stderr)
			}
			want := "fetch:/me\nfetch:/chats/chat\nfetch:/chats/chat/messages/image-message\n" +
				"fetch_blob:/chats/chat/messages/image-message/hostedContents/selected-content/$value\nfetch:/chats/chat/messages/image-message"
			if strings.Join(calls, "\n") != want {
				t.Fatalf("acquisition made extra/missing native calls: %v", calls)
			}
			doc := decode(t, out.String())
			data, err := os.ReadFile(output)
			if err != nil || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
				t.Fatalf("published output not complete PNG: %v", err)
			}
			digest := sha256.Sum256(data)
			if doc["status"] != "acquired" || doc["file_published"] != true || doc["output_verified"] != true ||
				doc["content_type"] != "image/png" || doc["width"] != float64(1) || doc["height"] != float64(1) ||
				doc["byte_size"] != float64(len(data)) || doc["sha256"] != hex.EncodeToString(digest[:]) ||
				doc["other_media"] != "not_acquired" || doc["scope"] != "selected_image" {
				t.Fatalf("receipt overclaimed or lost verified bytes: %s", &out)
			}
			if (doc["native_message_modified_at"] == nil) != (mode == "clockless") ||
				doc["native_body_observed_at"] == nil || doc["verification_observed_at"] == nil || doc["fetched_at"] == nil {
				t.Fatalf("missing clocks fabricated freshness or lost observations: %s", &out)
			}
			var retryOut, retryErr bytes.Buffer
			if retryCode := runCLI(context.Background(), imageFetchArgs(e, output), &retryOut, &retryErr); retryCode != 3 ||
				errorCode(t, retryErr.String()) != "image_output_exists" {
				t.Fatalf("published destination was overwritten: %d %s", retryCode, &retryErr)
			}
			if len(imageNativeCalls(t, transcript)) != len(calls) {
				t.Fatal("existing destination caused a duplicate native acquisition")
			}
		})
	}
}

type imageResultFaultWriter struct {
	cancel context.CancelFunc
}

func (w imageResultFaultWriter) Write([]byte) (int, error) {
	if w.cancel != nil {
		w.cancel()
		return 0, context.Canceled
	}
	return 0, io.ErrClosedPipe
}

func TestImagePublishedThenCancelledOrStdoutFailed(t *testing.T) {
	for _, mode := range []string{"stdout-failure", "cancelled", "both-streams-fail"} {
		t.Run(mode, func(t *testing.T) {
			e, _ := imageNativeEnv(t, "good")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writer := imageResultFaultWriter{}
			if mode == "cancelled" {
				writer.cancel = cancel
			}
			var stderr bytes.Buffer
			var errWriter io.Writer = &stderr
			if mode == "both-streams-fail" {
				errWriter = imageResultFaultWriter{}
			}
			output := filepath.Join(t.TempDir(), "retained.png")
			code := runCLI(ctx, imageFetchArgs(e, output), writer, errWriter)
			if code == 0 || code == 2 {
				t.Fatalf("result-write failure did not preserve nonzero outcome: %d %s", code, &stderr)
			}
			data, err := os.ReadFile(output)
			if err != nil || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
				t.Fatalf("post-publication failure removed complete destination: %v", err)
			}
			if mode == "both-streams-fail" {
				return
			}
			doc := decode(t, stderr.String())
			receipt, ok := doc["image_acquisition"].(map[string]any)
			if !ok || receipt["file_published"] != true || receipt["output_verified"] != true ||
				receipt["output"] != output || receipt["status"] != "failed" {
				t.Fatalf("post-publication receipt lost real effects: %s", &stderr)
			}
			if strings.Contains(stderr.String(), "nothing was half-written") {
				t.Fatalf("generic cancellation contradicted published image: %s", &stderr)
			}
			want := "image_receipt_failed"
			if mode == "cancelled" {
				want = "interrupted"
			}
			if errorCode(t, stderr.String()) != want {
				t.Fatalf("wrong post-publication error classification: %s", &stderr)
			}
		})
	}
}
