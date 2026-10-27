package workiq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

func TestMain(m *testing.M) {
	if os.Getenv("WORKIQ_TEST_ROLE") == "stub" {
		os.Exit(serveStub())
	}
	os.Exit(m.Run())
}

func serveStub() int {
	if reflect.DeepEqual(os.Args[1:], []string{"version"}) {
		switch os.Getenv("WORKIQ_TEST_MODE") {
		case "hang":
			time.Sleep(time.Minute)
		case "version-noisy":
			_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", maxStderr+100))
			return 0
		case "version-eula":
			_, _ = fmt.Fprintln(os.Stderr, "accept-eula private-download")
			return 1
		case "version-unknown":
			_, _ = fmt.Fprintln(os.Stdout, "private-download")
			return 0
		}
		_, _ = fmt.Fprintln(os.Stdout, "1.0.1")
		return 0
	}
	if !reflect.DeepEqual(os.Args[1:], []string{"mcp", "--account", "test.user@example.invalid"}) &&
		!reflect.DeepEqual(os.Args[1:], []string{"mcp"}) {
		return 80
	}
	mode := os.Getenv("WORKIQ_TEST_MODE")
	if mode == "stderr" {
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("x", 131072))
	}
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	var req struct {
		ID     int             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if dec.Decode(&req) != nil || req.Method != "initialize" {
		return 81
	}
	var init struct {
		Protocol string         `json:"protocolVersion"`
		Caps     map[string]any `json:"capabilities"`
	}
	if json.Unmarshal(req.Params, &init) != nil || init.Protocol != "2025-06-18" || len(init.Caps) != 0 {
		return 82
	}
	protocol := "2025-06-18"
	if mode == "old" {
		protocol = "2024-11-05"
	}
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
		"protocolVersion": protocol, "capabilities": map[string]any{"tools": map[string]any{}},
		"serverInfo": map[string]any{"name": "synthetic", "version": "1"},
	}})
	if dec.Decode(&req) != nil || req.Method != "notifications/initialized" {
		return 83
	}
	if mode == "idle" {
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": "idle-ping", "method": "ping"})
		var reply struct {
			ID     string          `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if dec.Decode(&reply) != nil || reply.ID != "idle-ping" || string(reply.Result) != "{}" {
			return 87
		}
		if os.WriteFile(os.Getenv("WORKIQ_TEST_ACK"), []byte("ack"), 0o600) != nil {
			return 88
		}
	}
	challenge := func() int {
		for _, method := range []string{"ping", "elicitation/create", "sampling/createMessage", "roots/list"} {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": "server-request", "method": method})
			var reply struct {
				ID     string              `json:"id"`
				Result json.RawMessage     `json:"result"`
				Error  *struct{ Code int } `json:"error"`
			}
			if dec.Decode(&reply) != nil || reply.ID != "server-request" {
				return 84
			}
			if method == "ping" && string(reply.Result) != "{}" || method != "ping" && (reply.Error == nil || reply.Error.Code != -32601) {
				return 85
			}
		}
		return 0
	}
	for dec.Decode(&req) == nil {
		var params struct {
			Name string `json:"name"`
			Args struct {
				URLs []string `json:"entityUrls"`
			} `json:"arguments"`
		}
		if req.Method != "tools/call" || json.Unmarshal(req.Params, &params) != nil || params.Name != "fetch" || len(params.Args.URLs) != 1 || !strings.HasPrefix(params.Args.URLs[0], "/") {
			return 86
		}
		if code := challenge(); code != 0 {
			return code
		}
		switch mode {
		case "eof":
			_, _ = fmt.Fprintln(os.Stderr, "accept-eula private-download")
			return 1
		case "notification":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{"text": "private-download"}})
		case "flood":
			for i := 0; i < 4097; i++ {
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/message"})
			}
		case "oversize":
			_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("x", maxFrame+100))
			continue
		case "rpc-error":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": "interaction_required private-download"}})
			continue
		case "missing-result":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID})
			continue
		}
		if mode == "hang" {
			time.Sleep(time.Minute)
		}
		if mode == "bad-json" {
			_, _ = fmt.Fprintln(os.Stdout, `{"id":1,"id":2}`)
			continue
		}
		cursor := "/me/messages?$skiptoken=A%2fb%2Bz&x=1"
		if mode == "secret-cursor" {
			cursor += "&tempauth=private-download"
		}
		id := req.ID
		if mode == "wrong-id" {
			id++
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"structuredContent": map[string]any{"results": []any{map[string]any{
				"statusCode": 200, "error": nil, "data": map[string]any{
					"id": "synthetic-item", "body": "retained text",
					"@microsoft.graph.downloadUrl": "https://file.invalid/?sig=private-download",
					"@odata.nextLink":              cursor,
				},
			}}},
		}})
		if mode == "extra-response" {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id + 1, "result": map[string]any{}})
		}
	}
	return 0
}

func useStub(t *testing.T, mode string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("M365CRAWL_WORKIQ_BIN", exe)
	t.Setenv("WORKIQ_TEST_ROLE", "stub")
	t.Setenv("WORKIQ_TEST_MODE", mode)
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var c *errs.Coded
	if !errors.As(err, &c) || c.Code != code || strings.Contains(err.Error(), "test.user@") || strings.Contains(err.Error(), "private-download") {
		t.Fatalf("want fixed %s, got %v", code, err)
	}
}

func TestWorkIQSpawnAdmission(t *testing.T) {
	t.Setenv("M365CRAWL_WORKIQ_BIN", "")
	for _, inv := range []Invocation{{}, {ExplicitTeamsRoot: true}, {ExplicitOutlookRoot: true}} {
		_, err := Open(context.Background(), inv, "")
		requireCode(t, err, "workiq_hermetic_required")
	}
	useStub(t, "")
	for _, address := range []string{"x&who@example.invalid", "x%PATH%@example.invalid", "x!y@example.invalid", "x\"@example.invalid", "x\n@example.invalid", ".x@example.invalid", "x..y@example.invalid", "x@-bad.invalid", "é@example.invalid"} {
		_, err := Open(context.Background(), Invocation{}, address)
		requireCode(t, err, "workiq_account_invalid")
	}
}

func TestWorkIQMCPTransport(t *testing.T) {
	for _, mode := range []string{"", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			useStub(t, mode)
			client, err := Open(context.Background(), Invocation{ExplicitTeamsRoot: true}, "test.user@example.invalid")
			if err != nil {
				t.Fatal(err)
			}
			page, err := client.Fetch(context.Background(), "/me/messages")
			if err != nil || page.StatusCode != 200 || page.NextCursor != "/me/messages?$skiptoken=A%2fb%2Bz&x=1" ||
				strings.Contains(string(page.JSON), "private-download") || !strings.Contains(string(page.JSON), "retained text") {
				t.Fatalf("fetch boundary: %+v %v (synthetic child: %v)", page, err, client.p.cmd.ProcessState)
			}
			if err := client.Close(); err != nil {
				t.Fatalf("close: %v (synthetic child: %v)", err, client.p.cmd.ProcessState)
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkIQCursorAndProtocolFailures(t *testing.T) {
	for mode, code := range map[string]string{
		"secret-cursor": "workiq_cursor_redacted", "wrong-id": "workiq_protocol",
		"bad-json": "workiq_protocol", "old": "workiq_protocol",
	} {
		t.Run(mode, func(t *testing.T) {
			useStub(t, mode)
			client, err := Open(context.Background(), Invocation{}, "")
			if err == nil {
				var page Page
				page, err = client.Fetch(context.Background(), "/me/messages")
				if len(page.JSON) != 0 {
					t.Fatal("failed page exposed data/cursor")
				}
				_ = client.Close()
			}
			requireCode(t, err, code)
		})
	}
}

func TestWorkIQCancellationAndVersion(t *testing.T) {
	useStub(t, "")
	v, err := Version(context.Background(), Invocation{})
	if err != nil || v != "1.0.1" {
		t.Fatalf("version: %q %v", v, err)
	}

	useStub(t, "hang")
	client, err := Open(context.Background(), Invocation{}, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = client.Fetch(ctx, "/me")
	requireCode(t, err, "workiq_timeout")
	_ = client.Close()
}

func TestWorkIQAnswersPingWhileIdle(t *testing.T) {
	useStub(t, "idle")
	ack := filepath.Join(t.TempDir(), "ping-ack")
	t.Setenv("WORKIQ_TEST_ACK", ack)
	client, err := Open(context.Background(), Invocation{}, "")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = client.Close() }()
	deadline := time.Now().Add(time.Second)
	for {
		if raw, err := os.ReadFile(ack); err == nil && string(raw) == "ack" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("idle MCP ping was never answered without a fetch")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWorkIQAbsoluteGraphCursorUsesRelativeTransport(t *testing.T) {
	useStub(t, "")
	client, err := Open(context.Background(), Invocation{}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if _, err := client.Fetch(context.Background(), "https://graph.microsoft.com/v1.0/me/messages?$skiptoken=A%2fb"); err != nil {
		t.Fatal(err)
	}
}
