//go:build windows

package diskspace

import "golang.org/x/sys/windows"

func free(dir string) (uint64, bool, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false, err
	}
	var avail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &totalFree); err != nil {
		return 0, false, err
	}
	return avail, true, nil
}
