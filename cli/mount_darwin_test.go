package main

import (
	"fmt"
	"strings"
	"testing"
)

// The nsmb.conf section plug adds is scoped to its server and added once.
func TestNsmbSectionIsScopedAndIdempotent(t *testing.T) {
	sec := fmt.Sprintf(nsmbSection, "127.0.0.1")
	if !strings.Contains(sec, "[127.0.0.1]\nmc_on=no\n") {
		t.Fatalf("section = %q", sec)
	}
	if strings.Contains(sec, "[default]") {
		t.Fatal("never the [default] section: another server's mounts are not plug's to configure")
	}
	if !hasNsmbSection([]byte("[default]\nsoft=yes\n"+sec), "127.0.0.1") {
		t.Fatal("the section must be found once written")
	}
	if hasNsmbSection([]byte("[default]\nmc_on=no\n[127.0.0.2]\nmc_on=no\n"), "127.0.0.1") {
		t.Fatal("another server's section is not ours")
	}
	if hasNsmbSection(nil, "127.0.0.1") {
		t.Fatal("an absent file has no section")
	}
}
