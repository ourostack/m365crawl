package hxstore

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

type failingReaderAt struct{}

func (failingReaderAt) ReadAt([]byte, int64) (int, error) { return 0, errors.New("disk failure") }

func TestOpenStoreRefusals(t *testing.T) {
	good := hxbuild.New(hxbuild.Options{}).Bytes()
	cases := map[string]struct {
		data         []byte
		code, detail string
	}{
		"version":     {hxbuild.New(hxbuild.Options{Version: 'j'}).Bytes(), CodeStoreVersion, "version byte 0x6a, known 0x69"},
		"page size":   {hxbuild.New(hxbuild.Options{PageSize: 8192}).Bytes(), CodeStoreVersion, "page size 8192, known 4096"},
		"not a store": {bytes.Repeat([]byte{'x'}, 128), CodeStoreUnrecognized, ""},
		"short":       {good[:10], CodeStoreUnrecognized, ""},
	}
	for name, c := range cases {
		_, err := OpenStore(bytes.NewReader(c.data), int64(len(c.data)))
		var g *GuardError
		if !errors.As(err, &g) || g.Code != c.code || g.Detail != c.detail || !strings.HasPrefix(err.Error(), c.code) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if s, err := OpenStore(bytes.NewReader(good), int64(len(good))); err != nil || s == nil {
		t.Fatalf("a good store: %v", err)
	}
	// A read failure is not a refusal.
	_, err := OpenStore(failingReaderAt{}, 1000)
	var g *GuardError
	if err == nil || errors.As(err, &g) {
		t.Fatalf("%v", err)
	}
}

func TestGuardErrorText(t *testing.T) {
	if (&GuardError{Code: "c"}).Error() != "c" || (&GuardError{Code: "c", Detail: "d"}).Error() != "c: d" {
		t.Fatal("error text")
	}
}
