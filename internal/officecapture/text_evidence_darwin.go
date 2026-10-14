//go:build darwin

package officecapture

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func heldTextEvidence(file *os.File) (FileEvidence, error) {
	var st unix.Stat_t
	if err := inputStat(int(file.Fd()), &st); err != nil {
		return FileEvidence{}, err
	}
	if st.Flags & ^uint32(permittedInputFlags) != 0 {
		return FileEvidence{}, errors.New("unqualified native flags")
	}
	gen := strconv.FormatUint(uint64(st.Gen), 10)
	creation := fmt.Sprintf("%d:%d", st.Btim.Sec, st.Btim.Nsec)
	modified := fmt.Sprintf("%d:%d", st.Mtim.Sec, st.Mtim.Nsec)
	device := int64(st.Dev)
	if device < 0 {
		device += 1 << 32
	}
	e := FileEvidence{VolumeID: strconv.FormatInt(device, 10), FileID: strconv.FormatUint(st.Ino, 10),
		GenerationPolicy: FileGenerationPolicy, NativeGenerationRaw: &gen, CreationRaw: &creation, ModificationRaw: &modified,
		Size: st.Size, NativeAttributes: strconv.FormatUint(uint64(st.Flags), 10)}
	at, known, err := nativeClock(modified)
	if err != nil {
		return FileEvidence{}, err
	}
	if known {
		if _, err := at.MarshalJSON(); err == nil {
			e.ModifiedAt = &at
		}
	}
	qualifyEvidence(&e)
	return e, ValidateFileEvidence(e)
}
