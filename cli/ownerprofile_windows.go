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

// guardKeyOwner refuses a key path that lies outside the profile of the account
// owning marker. Unknown owner, unknown profile, or an empty path: nothing is
// refused, exactly as before this check existed.
func guardKeyOwner(keyPath, marker string) {
	if err := keyOwnerError(keyPath, marker); err != nil {
		fatal("%v", err)
	}
}

// keyOwnerError is guardKeyOwner as a value. It is what the SYSTEM service
// calls from reconcile: that service holds every account's tunnels, and one
// account's mispointed key used to take all of them down (see refusal.go).
func keyOwnerError(keyPath, marker string) error {
	if keyPath == "" || marker == "" {
		return nil
	}
	profile := profileOfFileOwner(marker)
	if profile == "" {
		return nil // could not tell whose it is; unknown is not a refusal
	}
	abs, err := filepath.Abs(keyPath)
	if err != nil {
		return nil
	}
	if keyOutsideOwnersProfile(abs, profile) {
		return refuse("plug: refusing to read %s as a key.\n"+
			"      The client that asked for it was registered by an account whose profile is %s,\n"+
			"      and that key is outside it. plug runs as a machine-wide service here, so reading\n"+
			"      a file from another account's profile would be doing for one user something they\n"+
			"      cannot do themselves.", abs, profile)
	}
	return nil
}
