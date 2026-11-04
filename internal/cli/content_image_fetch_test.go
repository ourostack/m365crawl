package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
