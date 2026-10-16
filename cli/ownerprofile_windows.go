//go:build windows

package main

import (
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// profileOfFileOwner returns the profile directory of the account that OWNS
// path, which is how the SYSTEM service learns whose key it is being asked to
// read without believing anything the caller wrote.
//
// Windows records the creator of a file and no caller can make it name somebody
// else, so the owner of a marker under %ProgramData%\plug is the account that
// really registered that client. Its profile comes from ProfileList under HKLM,
// which only administrators can write. Neither half can be forged by the user
// this guard exists to constrain.
//
// Every failure returns "" and no error the caller must act on: an answer this
// cannot produce means "unknown", and the caller treats unknown as it treated
// everything before this existed. Refusing on a lookup that did not work would
// turn a service that reads one file too freely into a service that reads none.
func profileOfFileOwner(path string) string {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return ""
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return ""
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\`+owner.String(), registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	profile, _, err := k.GetStringValue("ProfileImagePath")
	if err != nil {
		return ""
	}
	// Expanded, because the stored value is often %SystemDrive%\Users\<name>.
	if expanded, err := registry.ExpandString(profile); err == nil {
		profile = expanded
	}
	return filepath.Clean(profile)
}
