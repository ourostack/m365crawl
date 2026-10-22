//go:build acceptance && windows

package acceptance

import (
	"reflect"
	"testing"
)

func TestOWAPrivateShapesDistinguishSurrogatesAndEmptyZonesWithoutValues(t *testing.T) {
	got := owaPrivateShapes(owaObject("Preview", "synthetic "+string([]byte{0xed, 0xa0, 0x80}), "StartTimeZoneId", ""))
	want := []owaPrivateShape{
		{"Preview", "string", "unpaired_utf16_code_unit", 1},
		{"StartTimeZoneId", "string", "empty_timezone", 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("private_encoding_or_zone_failure_category_missing")
	}
}
