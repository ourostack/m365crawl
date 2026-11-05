package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/imageexport"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestImageOutcomeCleanupErrorClassification(t *testing.T) {
	for _, code := range []string{"image_output_cleanup_failed", "image_output_cleanup_ownership_unknown", "image_native_close_failed"} {
		t.Run(code, func(t *testing.T) {
			var out, stderr bytes.Buffer
			rt := newRuntime(context.Background(), &Globals{}, &out, &stderr)
			rt.format = output.JSON
			published, verified := true, true
			failure := &imageFetchError{
				result: imageFetchResult{Status: "failed", Code: code, Scope: "selected_image",
					OtherMedia: "not_acquired", FilePublished: &published, OutputVerified: &verified},
				cause: &errs.Coded{Code: code, Exit: errs.ExitEnvironment, Message: "private phase failure"},
			}
			exit := rt.fail(failure)
			if exit != errs.ExitRuntime || out.Len() != 0 || strings.Contains(stderr.String(), "private phase failure") {
				t.Fatalf("cleanup/close failure misclassified or leaked its underlying cause: %d %s", exit, &stderr)
			}
			doc := decode(t, stderr.String())
			receipt := doc["image_acquisition"].(map[string]any)
			if doc["error"].(map[string]any)["code"] != code || receipt["file_published"] != true ||
				receipt["output_verified"] != true || receipt["status"] != "failed" {
				t.Fatalf("runtime cleanup failure lost the published side effect: %s", &stderr)
			}
		})
	}
}

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
	data, err := os.ReadFile(output) // #nosec G304 -- Exact pre-existing synthetic destination in t.TempDir.
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
	data, err := os.ReadFile(transcript) // #nosec G304 -- Exact synthetic transport transcript created in t.TempDir.
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
			data, err := os.ReadFile(output) // #nosec G304 -- Exact published synthetic PNG destination in t.TempDir.
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
			data, err := os.ReadFile(output) // #nosec G304 -- Exact retained synthetic PNG destination after controlled failure.
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

func TestImageFetchTextReceiptAndWriteFailure(t *testing.T) {
	e, _ := imageNativeEnv(t, "good")
	path := filepath.Join(t.TempDir(), "selected.png")
	var args []string
	for _, arg := range imageFetchArgs(e, path) {
		if arg != "--json" {
			args = append(args, arg)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runCLI(context.Background(), args, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("plain selected-image receipt failed: %d %s", code, &stderr)
	}
	doc := decode(t, stdout.String())
	if doc["status"] != "acquired" || doc["file_published"] != true ||
		doc["output_verified"] != true || doc["output"] != path || doc["scope"] != "selected_image" {
		t.Fatalf("plain output lost verified acquisition effects: %s", &stdout)
	}
	rt := newRuntime(context.Background(), &Globals{}, failWriter{}, &stderr)
	rt.format = output.Text
	if err := rt.write("content fetch image", imageFetchResult{Status: "acquired"}); !errors.Is(err, os.ErrClosed) {
		t.Fatal("plain receipt hid stdout failure", err)
	}
}

func TestImageOutcomeRetainedTempAndTextErrors(t *testing.T) {
	original := fetchWorkIQChatImage
	t.Cleanup(func() { fetchWorkIQChatImage = original })
	sentinel := errors.New("private native failure")
	verified := false
	fetchWorkIQChatImage = func(context.Context, syncer.WorkIQImageOptions) (syncer.WorkIQImageReport, error) {
		return syncer.WorkIQImageReport{
			Status: "failed", Code: "image_output_failed", Output: "/synthetic/selected.png",
			Effect: imageexport.Effect{Phase: "writing", OutputVerified: &verified,
				RetainedTemp: &imageexport.RetainedTemp{State: "partial", LastKnownPath: "/synthetic/retained",
					LocatorVerified: &verified}},
		}, sentinel
	}
	command := &contentFetchImageCmd{
		contentImagesCmd: contentImagesCmd{Target: "synthetic-chat", MessageID: "synthetic-message"},
		ImageID:          "synthetic-image", Output: "/synthetic/selected.png",
	}
	for _, format := range []output.Format{output.JSON, output.Text} {
		var stdout, stderr bytes.Buffer
		rt := newRuntime(context.Background(), &Globals{}, &stdout, &stderr)
		rt.format = format
		err := command.Run(rt)
		if !errors.Is(err, sentinel) || err.Error() != "selected image acquisition failed (image_output_failed)" {
			t.Fatal("acquisition error lost its safe description or underlying cause", err)
		}
		if code := rt.fail(err); code != errs.ExitRuntime || stdout.Len() != 0 ||
			strings.Contains(stderr.String(), "private native failure") {
			t.Fatalf("retained-output failure was hidden or leaked: %d %s", code, &stderr)
		}
		start := strings.Index(stderr.String(), `{"error":`)
		if start < 0 {
			t.Fatal("failure lost machine-readable acquisition receipt", &stderr)
		}
		receipt := decode(t, stderr.String()[start:])["image_acquisition"].(map[string]any)
		temp := receipt["retained_temp"].(map[string]any)
		if receipt["file_published"] != nil || receipt["output_verified"] != false ||
			receipt["phase"] != "writing" || temp["state"] != "partial" ||
			temp["last_known_path"] != "/synthetic/retained" || temp["locator_verified"] != false {
			t.Fatalf("retained/uncertain effect changed during CLI adaptation: %s", &stderr)
		}
		rt.stderr = failWriter{}
		if code := rt.fail(err); code != errs.ExitRuntime {
			t.Fatal("failed stderr was reported as success", code)
		}
	}
	for _, id := range []string{"bad|id", "https://example.invalid/image", "bad\nid"} {
		command.ImageID = id
		rt := newRuntime(context.Background(), &Globals{}, &bytes.Buffer{}, &bytes.Buffer{})
		var coded *errs.Coded
		if err := command.Run(rt); !errors.As(err, &coded) || coded.Exit != errs.ExitUsage {
			t.Fatal("unsafe image selector reached acquisition", err)
		}
	}
}
