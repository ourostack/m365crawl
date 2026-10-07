package outlookmail

import (
	"encoding/binary"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func at(day, hour int) time.Time { return time.Date(2031, 3, day, hour, 0, 0, 0, time.UTC) }

// obj turns a built object into the reader's Object, the way Walk hands it over.
func obj(o *hxbuild.Object) hxstore.Object {
	raw := o.Encode()[4:]
	return hxstore.Object{Raw: raw, Class: binary.LittleEndian.Uint16(raw[10:]), Tag: binary.LittleEndian.Uint16(raw[2:])}
}

// u32of converts a small test size to the word the builders take.
func u32of(n int) uint32 { return uint32(n) } //nolint:gosec // test sizes are small and non-negative
