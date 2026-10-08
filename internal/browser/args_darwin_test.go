//go:build darwin

package browser

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

func procArgsBuf(argc int32, path string, padding int, args ...string) []byte {
	buf := binary.NativeEndian.AppendUint32(nil, uint32(argc)) //nolint:gosec // G115: a test buffer, negative values wrap on purpose
	buf = append(buf, path...)
	buf = append(buf, 0)
	buf = append(buf, make([]byte, padding)...)
	for _, a := range args {
		buf = append(buf, a...)
		buf = append(buf, 0)
	}
	return append(buf, "HOME=/x\x00"...)
}

func TestParseProcArgs(t *testing.T) {
	argv, err := parseProcArgs(procArgsBuf(3, "/usr/bin/edge", 3, "edge", "--user-data-dir=/a b/c", "about:blank"))
	if err != nil || strings.Join(argv, "|") != "edge|--user-data-dir=/a b/c|about:blank" {
		t.Fatalf("argv = %q %v", argv, err)
	}
	for name, buf := range map[string][]byte{
		"short":         {1, 0},
		"no exec path":  append(binary.NativeEndian.AppendUint32(nil, 1), "abc"...),
		"argc too big":  procArgsBuf(9, "/x", 1, "a"),
		"negative argc": procArgsBuf(-1, "/x", 1, "a"),
	} {
		if _, err := parseProcArgs(buf); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func TestReadProcArgsReal(t *testing.T) {
	argv, err := readProcArgs(os.Getpid())
	if err != nil || len(argv) == 0 {
		t.Fatalf("own arguments: %q %v", argv, err)
	}
	if _, err := readProcArgs(2147483646); err == nil {
		t.Fatal("a process that does not exist has no arguments")
	}
}
