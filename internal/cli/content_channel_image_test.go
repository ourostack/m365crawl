package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFetchChannelRootImage(t *testing.T) {
	for _, selection := range []struct{ image, hosted string }{
		{"synthetic-image", "selected-content"},
		{"second-image", "second-content"},
	} {
		t.Run(selection.image, func(t *testing.T) {
			e, transcript := imageNativeEnv(t, "channel-good")
			output := filepath.Join(imageOutputDir(t), "selected.png")
			args := imageFetchArgs(e, output)
			args[slices.Index(args, "synthetic-image")] = selection.image
			args = append(args, "--team-id", "synthetic-team")
			var out, stderr bytes.Buffer
			code := runCLI(context.Background(), args, &out, &stderr)
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("selected channel root PNG refused: %d %s %s", code, &out, &stderr)
			}
			want := "fetch:/me\nfetch:/teams/synthetic-team/channels/chat\n" +
				"fetch:/teams/synthetic-team/channels/chat/messages/image-message\n" +
				"fetch_blob:/teams/synthetic-team/channels/chat/messages/image-message/hostedContents/" + selection.hosted + "/$value\n" +
				"fetch:/teams/synthetic-team/channels/chat/messages/image-message"
			if calls := strings.Join(imageNativeCalls(t, transcript), "\n"); calls != want {
				t.Fatalf("channel acquisition escaped exact selected scope: %s", calls)
			}
			doc := decode(t, out.String())
			data, err := os.ReadFile(output) // #nosec G304 -- Exact synthetic PNG destination owned by this test.
			digest := sha256.Sum256(data)
			if err != nil || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) ||
				doc["sha256"] != hex.EncodeToString(digest[:]) || doc["byte_size"] != float64(len(data)) ||
				doc["team_id"] != "synthetic-team" || doc["conversation_id"] != "chat" ||
				doc["message_id"] != "image-message" || doc["image_id"] != selection.image ||
				doc["account"] != "synthetic-tenant/synthetic-user" || doc["file_published"] != true ||
				doc["output_verified"] != true || doc["scope"] != "selected_image" || doc["other_media"] != "not_acquired" ||
				doc["native_message_modified_at"] == nil || doc["native_body_observed_at"] == nil ||
				doc["verification_observed_at"] == nil || doc["fetched_at"] == nil {
				t.Fatalf("channel receipt lost selected provenance/integrity: %s %v", &out, err)
			}
		})
	}
}

func TestFetchChannelImageRefusals(t *testing.T) {
	for _, tc := range []struct {
		mode, code string
		blob       bool
	}{
		{"wrong-user", "workiq_identity_mismatch", false},
		{"invalid-address", "workiq_account_invalid", false},
		{"wrong-tenant", "workiq_chat_account_unmatched", false},
		{"missing-tenant", "workiq_chat_account_unmatched", false},
		{"wrong-channel", "image_reference_unsupported", false},
		{"malformed-channel", "workiq_protocol", false},
		{"fetch-channel-failed", "workiq_failed", false},
		{"shared", "image_kind_unsupported", false},
		{"unknown-membership", "image_kind_unsupported", false},
		{"missing-membership", "image_kind_unsupported", false},
		{"wrong-team", "image_reference_unsupported", false},
		{"wrong-message-channel", "image_reference_unsupported", false},
		{"wrong-message", "image_reference_unsupported", false},
		{"missing-identity", "image_reference_unsupported", false},
		{"reply", "image_reference_unsupported", false},
		{"deleted", "image_reference_unsupported", false},
		{"removed", "image_reference_unsupported", false},
		{"unsafe-reference", "image_reference_unsupported", false},
		{"changed-reference", "image_reference_changed", true},
		{"changed-clock", "image_reference_changed", true},
		{"lost-clock", "image_reference_changed", true},
		{"deleted-after-blob", "image_reference_unsupported", true},
		{"wrong-team-after-blob", "image_reference_unsupported", true},
		{"wrong-channel-after-blob", "image_reference_unsupported", true},
		{"missing-identity-after-blob", "image_reference_unsupported", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			e, transcript := imageNativeEnv(t, "channel-"+tc.mode)
			dir := imageOutputDir(t)
			args := append(imageFetchArgs(e, filepath.Join(dir, "selected.png")), "--team-id", "synthetic-team")
			var out, stderr bytes.Buffer
			code := runCLI(context.Background(), args, &out, &stderr)
			if code == 0 || code == 2 || out.Len() != 0 || errorCode(t, stderr.String()) != tc.code {
				t.Fatalf("wrong channel admission outcome: %d %s %s", code, &out, &stderr)
			}
			calls := strings.Join(imageNativeCalls(t, transcript), "\n")
			if strings.Contains(calls, "fetch_blob:") != tc.blob {
				t.Fatalf("blob admission order wrong: %s", calls)
			}
			receipt := decode(t, stderr.String())["image_acquisition"].(map[string]any)
			if receipt["file_published"] != false || receipt["team_id"] != "synthetic-team" {
				t.Fatalf("failed channel acquisition lost effects/scope: %s", &stderr)
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Fatal("failed admission published output or retained reservation", entries, err)
			}
		})
	}
}

func TestChannelImageTeamSelectors(t *testing.T) {
	link := "https://teams.microsoft.com/l/message/chat/image-message"
	for _, tc := range []struct {
		name, target, team string
		want               int
	}{
		{"link-team", link + "?groupId=synthetic-team", "", 0},
		{"matching-team", link + "?groupId=synthetic-team", "synthetic-team", 0},
		{"explicit-team", link, "synthetic-team", 0},
		{"conflict", link + "?groupId=other-team", "synthetic-team", 2},
		{"invalid-explicit", link, "../team", 2},
		{"invalid-link", link + "?groupId=%2Fteam", "", 2},
		{"double-encoded-link", link + "?groupId=%252Fteam", "", 2},
		{"empty-link", link + "?groupId=", "", 2},
		{"duplicate-link", link + "?groupId=synthetic-team&groupId=other-team", "", 2},
		{"malformed-query", link + "?groupId=%zz", "", 2},
		{"encoded-malformed-team", link + "?group%49d=%zz", "", 2},
		{"reply-link", link + "?groupId=synthetic-team&parentMessageId=other-message", "", 2},
		{"root-link", link + "?groupId=synthetic-team&parentMessageId=image-message", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, transcript := imageNativeEnv(t, "channel-good")
			output := filepath.Join(imageOutputDir(t), "selected.png")
			args := []string{"--db", e.db, "--teams-root", e.root, "--json",
				"--account", "synthetic-tenant/synthetic-user", "content", "fetch", "image", tc.target,
				"--image-id", "synthetic-image", "--output", output}
			if tc.team != "" {
				args = append(args, "--team-id", tc.team)
			}
			var out, stderr bytes.Buffer
			if code := runCLI(context.Background(), args, &out, &stderr); code != tc.want {
				t.Fatalf("team selector outcome %d, want %d: %s", code, tc.want, &stderr)
			}
			if tc.want != 0 && len(imageNativeCalls(t, transcript)) != 0 {
				t.Fatal("invalid selector reached native transport")
			}
		})
	}
}

func TestChatImageMalformedUnrelatedQueryKeepsChatMode(t *testing.T) {
	for _, query := range []string{"ignored=%zz", "ignored=one;two"} {
		cmd := contentFetchImageCmd{contentImagesCmd: contentImagesCmd{
			Target: "https://teams.microsoft.com/l/message/chat/image-message?" + query,
		}}
		team, err := cmd.teamSelector("image-message")
		if err != nil || team != "" {
			t.Fatal("unrelated query changed existing chat-mode selection", err)
		}
	}
}
