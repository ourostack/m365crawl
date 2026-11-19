package workiq

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const channelImageTestPath = "/teams/synthetic-team/channels/19:synthetic-channel/messages/1001/hostedContents/opaque%3Dvalue/$value"

func channelImageMessage(path string) json.RawMessage {
	var wire map[string]any
	_ = json.Unmarshal(imageTestMessage(path, "2026-01-01T00:00:00Z"), &wire)
	wire["channelIdentity"] = map[string]any{"teamId": "synthetic-team", "channelId": "19:synthetic-channel"}
	raw, _ := json.Marshal(wire)
	return raw
}

func TestSelectedImageChannelAdmission(t *testing.T) {
	ctx := context.Background()
	for _, membership := range []string{"standard", "private"} {
		channel, err := DecodeImageChannel(ctx, json.RawMessage(`{"id":"channel","tenantId":"tenant","membershipType":"`+membership+`"}`))
		if err != nil || channel.ID != "channel" || channel.TenantID != "tenant" {
			t.Fatal("supported native channel identity lost", err)
		}
	}
	for _, raw := range []string{
		`{`, `{}`, `{"id":"../channel","membershipType":"standard"}`,
		`{"id":"channel","membershipType":"shared"}`, `{"id":"channel","membershipType":"unknownFutureValue"}`,
		`{"id":"channel"}`,
	} {
		if _, err := DecodeImageChannel(ctx, json.RawMessage(raw)); err == nil {
			t.Fatal("unknown or malformed channel admitted")
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := DecodeImageChannel(ctx, json.RawMessage(`{"id":"channel","membershipType":"standard"}`)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled channel identity admitted", err)
	}
}

func TestSelectedChannelNativePaths(t *testing.T) {
	for _, path := range []string{
		"/teams/team/channels/19:channel",
		"/teams/team/channels/19%3Achannel/messages/1001",
	} {
		got, err := RelativePath(path, "")
		if err != nil || got != path {
			t.Fatal("exact channel/root native fetch refused", err)
		}
	}
	for _, path := range []string{
		"/teams", "/teams/team/channels", "/teams/team/channels/channel/messages",
		"/teams/team/channels/channel/members/member",
		"/teams/team/channels/channel/messages/1001/replies/1002",
		"/teams/team/channels/channel/messages/1001?query=value",
		"/teams/team/channels/channel?", "/teams/team/channels/%2e%2e",
		"/teams/team/channels/%2Fchannel", "/teams/team/channels/%252Fchannel",
		"/teams/team/channels/chan%7Cnel", "/teams/team/channels/",
	} {
		if _, err := RelativePath(path, ""); err == nil {
			t.Fatal("unselected or escaping channel route admitted")
		}
	}
}

func TestSelectedChannelImageScope(t *testing.T) {
	ctx := context.Background()
	raw := channelImageMessage(channelImageTestPath)
	for _, path := range []string{channelImageTestPath, "https://graph.microsoft.com/v1.0" + channelImageTestPath} {
		message, err := DecodeSelectedChannelImageMessage(ctx, "synthetic-team", "19:synthetic-channel", "1001", channelImageMessage(path))
		if err != nil {
			t.Fatal(err)
		}

		ref, err := BindChatImage(message, "synthetic-image")
		if err != nil || ref.resource != channelImageTestPath {
			t.Fatal("channel hosted-content scope/encoding not preserved", err)
		}
		if err := SameImageObservation(message, message, "synthetic-image"); err != nil {
			t.Fatal("stable channel observation refused", err)
		}
	}
	for _, team := range []string{"", "../team", "team%2Fother"} {
		if _, err := DecodeSelectedChannelImageMessage(ctx, team, "19:synthetic-channel", "1001", raw); err == nil {
			t.Fatal("invalid team selector admitted")
		}
	}
	for _, change := range []func(map[string]any){
		func(w map[string]any) { delete(w, "channelIdentity") },
		func(w map[string]any) { w["channelIdentity"] = nil },
		func(w map[string]any) { w["channelIdentity"] = "invalid" },
		func(w map[string]any) {
			w["channelIdentity"] = map[string]any{"teamId": "other", "channelId": "19:synthetic-channel"}
		},
		func(w map[string]any) {
			w["channelIdentity"] = map[string]any{"teamId": "synthetic-team", "channelId": "other"}
		},
		func(w map[string]any) { w["replyToId"] = "999" },
		func(w map[string]any) { w["id"] = "1002" },
		func(w map[string]any) { w["deletedDateTime"] = "2026-01-01T00:01:00Z" },
		func(w map[string]any) { w["@removed"] = map[string]any{} },
	} {
		var wire map[string]any
		_ = json.Unmarshal(raw, &wire)
		change(wire)
		bad, _ := json.Marshal(wire)
		if _, err := DecodeSelectedChannelImageMessage(ctx, "synthetic-team", "19:synthetic-channel", "1001", bad); err == nil {
			t.Fatal("unbound or non-root channel observation admitted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := DecodeSelectedChannelImageMessage(cancelled, "synthetic-team", "19:synthetic-channel", "1001", raw); !errors.Is(err, context.Canceled) {
		t.Fatal("channel cancellation lost", err)
	}
	for _, path := range []string{
		imageTestPath,
		strings.Replace(channelImageTestPath, "synthetic-team", "other-team", 1),
		strings.Replace(channelImageTestPath, "synthetic-channel", "other-channel", 1),
		strings.Replace(channelImageTestPath, "/1001/", "/1002/", 1),
		strings.Replace(channelImageTestPath, "/messages/", "/replies/", 1),
		strings.Replace(channelImageTestPath, "hostedContents", "attachments", 1),
		strings.Replace(channelImageTestPath, "opaque%3Dvalue", "%2Fescape", 1),
		strings.Replace(channelImageTestPath, "opaque%3Dvalue", "%5Cescape", 1),
		strings.Replace(channelImageTestPath, "opaque%3Dvalue", "%252Fescape", 1),
		strings.Replace(channelImageTestPath, "opaque%3Dvalue", "%2e%2e", 1),
		strings.Replace(channelImageTestPath, "opaque%3Dvalue", "..", 1),
		strings.Replace(channelImageTestPath, "opaque%3Dvalue", "", 1),
		channelImageTestPath + "?sig=synthetic", channelImageTestPath + "#fragment",
		"https://graph.microsoft.com:443/v1.0" + channelImageTestPath,
		"https://user@graph.microsoft.com/v1.0" + channelImageTestPath,
		"https://example.invalid" + channelImageTestPath,
		"https://graph.microsoft.com/beta" + channelImageTestPath,
		"//graph.microsoft.com/v1.0" + channelImageTestPath,
		strings.Replace(channelImageTestPath, "/$value", "", 1),
	} {
		message, err := DecodeSelectedChannelImageMessage(ctx, "synthetic-team", "19:synthetic-channel", "1001", channelImageMessage(path))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := BindChatImage(message, "synthetic-image"); err == nil {
			t.Fatal("channel resource escaped selected scope")
		}
	}
}
