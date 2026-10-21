//go:build acceptance && windows

package acceptance

import (
	"context"
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type owaWindowsDynamicZone struct {
	windows.Timezoneinformation
	KeyName          [128]uint16
	DaylightDisabled uint8
}

type owaWindowsZoneOracle struct {
	zones map[string]owaWindowsDynamicZone
}

var (
	owaNativeZoneEnum = windows.NewLazySystemDLL("advapi32.dll").NewProc("EnumDynamicTimeZoneInformation")
	owaNativeToLocal  = windows.NewLazySystemDLL("kernel32.dll").NewProc("SystemTimeToTzSpecificLocalTimeEx")
)

func owaNewWindowsZoneOracle(ctx context.Context) (*owaWindowsZoneOracle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if owaNativeZoneEnum.Find() != nil || owaNativeToLocal.Find() != nil {
		return nil, errors.New("calendar_oracle_native_refused")
	}
	oracle := &owaWindowsZoneOracle{zones: map[string]owaWindowsDynamicZone{}}
	for index := uintptr(0); index < 512; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var zone owaWindowsDynamicZone
		// #nosec G103 -- Native system API writes one ABI-checked 432-byte dynamic-timezone structure; no source payload is passed.
		status, _, _ := owaNativeZoneEnum.Call(index, uintptr(unsafe.Pointer(&zone)))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if status == uintptr(windows.ERROR_NO_MORE_ITEMS) {
			if len(oracle.zones) == 0 {
				return nil, errors.New("calendar_oracle_native_refused")
			}
			return oracle, nil
		}
		if status != 0 {
			return nil, errors.New("calendar_oracle_native_refused")
		}
		name := windows.UTF16ToString(zone.KeyName[:])
		if name == "" {
			return nil, errors.New("calendar_oracle_native_refused")
		}
		if _, duplicate := oracle.zones[name]; duplicate {
			return nil, errors.New("calendar_oracle_native_refused")
		}
		oracle.zones[name] = zone
	}
	return nil, errors.New("calendar_oracle_zone_budget_refused")
}

func (oracle *owaWindowsZoneOracle) local(ctx context.Context, name string, instant time.Time) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	zone, found := oracle.zones[name]
	if !found {
		return time.Time{}, errors.New("calendar_oracle_zone_unknown")
	}
	utc := instant.UTC()
	if utc.Year() < 1601 || utc.Year() > 9999 {
		return time.Time{}, errors.New("calendar_oracle_native_year_refused")
	}
	// #nosec G115 -- Year is bounded above; Go clock/calendar fields and milliseconds are intrinsically within uint16.
	input := windows.Systemtime{
		Year: uint16(utc.Year()), Month: uint16(utc.Month()), Day: uint16(utc.Day()),
		Hour: uint16(utc.Hour()), Minute: uint16(utc.Minute()), Second: uint16(utc.Second()),
		Milliseconds: uint16(utc.Nanosecond() / int(time.Millisecond)),
	}
	var output windows.Systemtime
	// #nosec G103 -- Fixed native system API reads the ABI-checked timezone/UTC structures and writes one Systemtime.
	ok, _, _ := owaNativeToLocal.Call(uintptr(unsafe.Pointer(&zone)), uintptr(unsafe.Pointer(&input)), uintptr(unsafe.Pointer(&output)))
	if ok == 0 {
		return time.Time{}, errors.New("calendar_oracle_native_refused")
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	wall := time.Date(int(output.Year), time.Month(output.Month), int(output.Day), int(output.Hour), int(output.Minute),
		int(output.Second), int(output.Milliseconds)*int(time.Millisecond), time.UTC)
	if wall.Year() != int(output.Year) || wall.Month() != time.Month(output.Month) || wall.Day() != int(output.Day) ||
		wall.Hour() != int(output.Hour) || wall.Minute() != int(output.Minute) || wall.Second() != int(output.Second) ||
		output.Milliseconds > 999 {
		return time.Time{}, errors.New("calendar_oracle_native_refused")
	}
	offset := wall.Sub(utc.Truncate(time.Millisecond))
	if offset < -24*time.Hour || offset > 24*time.Hour || offset%time.Second != 0 {
		return time.Time{}, errors.New("calendar_oracle_native_refused")
	}
	return instant.In(time.FixedZone("Windows oracle", int(offset/time.Second))), nil
}
