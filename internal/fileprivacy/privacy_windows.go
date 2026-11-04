//go:build windows

package fileprivacy

import "golang.org/x/sys/windows"

var (
	privateGetSecurity = windows.GetSecurityInfo
	privateTokenUser   = func() (*windows.Tokenuser, error) {
		return windows.GetCurrentProcessToken().GetTokenUser()
	}
)
