package browsertest

import (
	"fmt"
	"testing"
)

func TestServerNavigationLoaderBinding(t *testing.T) {
	s := NewServer(t)
	c := dial(t, s)
	frame := func(id int) map[string]any {
		t.Helper()
		result := roundTrip(t, c, fmt.Sprintf(`{"id":%d,"method":"Page.getFrameTree","sessionId":"S1"}`, id))["result"].(map[string]any)
		return result["frameTree"].(map[string]any)["frame"].(map[string]any)
	}
	initial := frame(1)
	id, previous := initial["id"], initial["loaderId"]
	if id == "" || previous == "" || id == nil || previous == nil {
		t.Fatal("initial committed document has no binding", initial)
	}
	for n := 0; n < 2; n++ {
		ack := roundTrip(t, c, fmt.Sprintf(`{"id":%d,"method":"Page.navigate","sessionId":"S1","params":{"url":"https://example.test/requested"}}`, 2+n*2))["result"].(map[string]any)
		committed := frame(3 + n*2)
		if ack["frameId"] != id || committed["id"] != id ||
			ack["loaderId"] == nil || ack["loaderId"] == "" || ack["loaderId"] == previous ||
			committed["loaderId"] != ack["loaderId"] {
			t.Fatal("navigation acknowledgment and actual committed loader diverge", ack, committed)
		}
		previous = ack["loaderId"]
	}
}
