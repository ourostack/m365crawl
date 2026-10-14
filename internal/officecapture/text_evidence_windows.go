//go:build windows

package officecapture

import (
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

func heldTextEvidence(file *os.File) (FileEvidence, error) {
	var st windows.ByHandleFileInformation
	if err := inputWinInfo(windows.Handle(file.Fd()), &st); err != nil {
		return FileEvidence{}, err
	}
	if st.FileSizeHigh != 0 || st.FileSizeLow > 64<<20 {
		return FileEvidence{}, &ReadError{Code: "local_text_too_large"}
	}
	creation := strconv.FormatUint(uint64(st.CreationTime.HighDateTime)<<32|uint64(st.CreationTime.LowDateTime), 10)
	modified := strconv.FormatUint(uint64(st.LastWriteTime.HighDateTime)<<32|uint64(st.LastWriteTime.LowDateTime), 10)
	e := FileEvidence{VolumeID: strconv.FormatUint(uint64(st.VolumeSerialNumber), 10),
		FileID:           strconv.FormatUint(uint64(st.FileIndexHigh)<<32|uint64(st.FileIndexLow), 10),
		GenerationPolicy: FileGenerationPolicy, CreationRaw: &creation, ModificationRaw: &modified,
		Size: int64(st.FileSizeLow), NativeAttributes: strconv.FormatUint(uint64(st.FileAttributes), 10)}
	at, known, _ := nativeClock(modified)
	if known {
		if _, err := at.MarshalJSON(); err == nil {
			e.ModifiedAt = &at
		}
	}
	qualifyEvidence(&e)
	return e, ValidateFileEvidence(e)
}
