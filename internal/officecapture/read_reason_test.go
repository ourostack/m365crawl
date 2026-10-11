package officecapture

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

func TestJSONCaptureKeepsFixedReaderRefusalReason(t *testing.T) {
	path, _ := ordinaryFixture(t)
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"}, func(c *JSONCapture) error {
		_, err := c.ReadJSON(context.Background())
		return err
	})
	var coded *ReadError
	if !errors.As(err, &coded) || coded.Code != "office_json_unrecognized" {
		t.Fatal("missing envelope was collapsed into unknown capture error")
	}
}

func TestJSONCaptureCallbackReasonsAreFixedAndCallerCancellationPreserved(t *testing.T) {
	for _, sample := range []struct {
		cause error
		want  string
	}{
		{&ReadError{Code: "office_json_policy_restore_failed"}, "office_json_policy_restore_failed"},
		{&ReadError{Code: "private-path-or-error"}, "office_json_capture_failed"},
		{&officedocuments.ReadError{Code: "private-reader-error"}, "office_json_capture_failed"},
		{errors.New("private callback data"), "office_json_capture_failed"},
	} {
		path, _ := ordinaryFixture(t)
		err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: "recent", Application: "supplied"},
			func(*JSONCapture) error { return sample.cause })
		var coded *ReadError
		if !errors.As(err, &coded) || coded.Code != sample.want || errors.Is(err, sample.cause) {
			t.Fatal("untrusted callback error escaped fixed public-safe reason boundary")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := readerFailure(ctx, &ReadError{Code: "office_json_input_unqualified"}); !errors.Is(err, context.Canceled) {
		t.Fatal("fixed reason lost caller cancellation")
	}
}
