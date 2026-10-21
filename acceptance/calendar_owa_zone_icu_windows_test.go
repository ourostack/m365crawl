//go:build acceptance && windows

package acceptance

import (
	"context"
	"errors"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

type owaNativeZoneIdentity struct {
	Windows, Canonical, Representative string
	Alias                              bool
}

var (
	owaSystemICU      = windows.NewLazySystemDLL("icu.dll")
	owaICUCanonical   = owaSystemICU.NewProc("ucal_getCanonicalTimeZoneID")
	owaICUToWindows   = owaSystemICU.NewProc("ucal_getWindowsTimeZoneID")
	owaICUFromWindows = owaSystemICU.NewProc("ucal_getTimeZoneIDForWindowsID")
)

func owaICUInput(name string) ([]uint16, error) {
	if name == "" || !utf8.ValidString(name) {
		return nil, errors.New("calendar_oracle_zone_unknown")
	}
	input, err := windows.UTF16FromString(name)
	if err != nil || len(input) > 256 {
		return nil, errors.New("calendar_oracle_zone_unknown")
	}
	return input, nil
}

func owaICUOutput(ctx context.Context, count uintptr, status int32, output []uint16) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if status == 1 {
		return "", errors.New("calendar_oracle_zone_unknown")
	}
	if status > 0 || count >= uintptr(len(output)) {
		return "", errors.New("calendar_oracle_icu_refused")
	}
	if count == 0 {
		return "", errors.New("calendar_oracle_zone_unmapped")
	}
	return windows.UTF16ToString(output[:count]), nil
}

func owaICUWindowsIANA(ctx context.Context, name string) (string, error) {
	input, err := owaICUInput(name)
	if err != nil {
		return "", err
	}
	var output [256]uint16
	var status int32
	// #nosec G103 -- Documented ICU C ABI: bounded UTF-16 input/output, null world-default region and one int32 status.
	count, _, _ := owaICUFromWindows.Call(uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)-1), 0,
		uintptr(unsafe.Pointer(&output[0])), uintptr(len(output)), uintptr(unsafe.Pointer(&status)))
	return owaICUOutput(ctx, count, status, output[:])
}

func (oracle *owaWindowsZoneOracle) identity(ctx context.Context, name string) (owaNativeZoneIdentity, error) {
	if err := ctx.Err(); err != nil {
		return owaNativeZoneIdentity{}, err
	}
	if owaICUCanonical.Find() != nil || owaICUToWindows.Find() != nil || owaICUFromWindows.Find() != nil {
		return owaNativeZoneIdentity{}, errors.New("calendar_oracle_icu_refused")
	}
	if _, found := oracle.zones[name]; found {
		representative, err := owaICUWindowsIANA(ctx, name)
		if err != nil {
			return owaNativeZoneIdentity{}, err
		}
		return owaNativeZoneIdentity{Windows: name, Canonical: representative, Representative: representative}, nil
	}
	input, err := owaICUInput(name)
	if err != nil {
		return owaNativeZoneIdentity{}, err
	}
	var canonical [256]uint16
	var status int32
	var systemID uint8
	// #nosec G103 -- ICU writes a bounded canonical UTF-16 identifier, one-byte UBool and int32 status; no default zone is requested.
	count, _, _ := owaICUCanonical.Call(uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)-1),
		uintptr(unsafe.Pointer(&canonical[0])), uintptr(len(canonical)), uintptr(unsafe.Pointer(&systemID)), uintptr(unsafe.Pointer(&status)))
	canonicalName, err := owaICUOutput(ctx, count, status, canonical[:])
	if err != nil {
		return owaNativeZoneIdentity{}, err
	}
	if systemID != 1 {
		return owaNativeZoneIdentity{}, errors.New("calendar_oracle_zone_unknown")
	}
	var key [256]uint16
	status = 0
	// #nosec G103 -- Documented ICU C ABI reads the bounded canonical UTF-16 identifier and writes one Windows key/status.
	count, _, _ = owaICUToWindows.Call(uintptr(unsafe.Pointer(&canonical[0])), count, uintptr(unsafe.Pointer(&key[0])),
		uintptr(len(key)), uintptr(unsafe.Pointer(&status)))
	windowsKey, err := owaICUOutput(ctx, count, status, key[:])
	if err != nil {
		return owaNativeZoneIdentity{}, err
	}
	if _, found := oracle.zones[windowsKey]; !found {
		return owaNativeZoneIdentity{}, errors.New("calendar_oracle_zone_unmapped")
	}
	representative, err := owaICUWindowsIANA(ctx, windowsKey)
	if err != nil {
		return owaNativeZoneIdentity{}, err
	}
	return owaNativeZoneIdentity{Windows: windowsKey, Canonical: canonicalName, Representative: representative, Alias: canonicalName != name}, nil
}
