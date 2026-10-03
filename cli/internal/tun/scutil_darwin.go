//go:build darwin

package tun

import (
	"fmt"
	"os/exec"
	"strings"
)

// The SystemConfiguration dynamic store, as plug reads and writes it: the
// scutil primitives and the parsing of what they print. No policy lives here
// (which keys plug overrides, what counts as a leftover of its own, what to put
// back at teardown): that is route_darwin.go's. This file only knows how to ask
// configd a question and read the answer.

// scutil pipes a batch script into scutil (root; the plug core runs under sudo),
// used for dynamic-store reads and edits. A var so a test can stand a fake
// store behind the readers (poisonedKeys, PoisonedViews) and describe the
// machine it wants instead of reading the one it runs on.
var scutil = func(script string) (string, error) {
	cmd := exec.Command(HelperPath("scutil"))
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// scutilSet writes the dictionary accumulated by build (d.init/d.add lines) into
// key of the dynamic store.
// A var, like its neighbour below, so the orphan recovery can be tested. That
// path repairs the resolver of the WHOLE MACHINE after a daemon dies without
// unwinding, and nothing exercised it: the order it replays the three backups
// in is the difference between a working resolver and one pointing at a dead
// address, and no test could see that order.
var scutilSet = func(key, build string) error {
	_, err := scutil(build + "set " + key + "\nquit\n")
	return err
}

// scutilRemove deletes key from the dynamic store.
var scutilRemove = func(key string) error {
	_, err := scutil("remove " + key + "\nquit\n")
	return err
}

// primaryService returns the id of the primary network service (the one whose
// DNS resolves bare names) from the dynamic store.
func primaryService() (string, error) {
	out, err := scutil("show State:/Network/Global/IPv4\nquit\n")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PrimaryService :"); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("no PrimaryService in State:/Network/Global/IPv4")
}

// readDNSDict reads the DNS dict at key and returns (a) a scutil script that
// rebuilds it verbatim (d.init + d.add lines) for restore, and (b) its
// ServerAddresses, plug's upstream for dotted names. Both are empty if the key
// is absent. It parses scutil's show output: scalars ("Key : value") and arrays
// ("Key : <array> { N : value ... }").
func readDNSDict(key string) (restore string, servers, search []string) {
	out, err := scutil("show " + key + "\nquit\n")
	if err != nil || strings.Contains(out, "No such key") {
		return "", nil, nil
	}
	var b strings.Builder
	b.WriteString("d.init\n")
	var curKey string
	var arr []string
	inArray := false
	flushArray := func() {
		if curKey == "" {
			return
		}
		b.WriteString("d.add " + curKey + " * " + strings.Join(arr, " ") + "\n")
		if curKey == "ServerAddresses" {
			servers = append(servers, arr...)
		}
		if curKey == "SearchDomains" {
			search = append(search, arr...)
		}
		curKey, arr, inArray = "", nil, false
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || strings.HasPrefix(line, "<dictionary>"):
			continue
		case strings.Contains(line, ": <array> {"):
			curKey = strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
			arr, inArray = nil, true
		case line == "}":
			if inArray {
				flushArray()
			}
		case inArray:
			if p := strings.SplitN(line, ":", 2); len(p) == 2 {
				arr = append(arr, strings.TrimSpace(p[1]))
			}
		default: // scalar "Key : value"
			if p := strings.SplitN(line, ":", 2); len(p) == 2 {
				b.WriteString("d.add " + strings.TrimSpace(p[0]) + " " + strings.TrimSpace(p[1]) + "\n")
			}
		}
	}
	return b.String(), servers, search
}

// readScopedDict returns a DNS dict's servers and its SupplementalMatchDomains.
// A dict with no match domains is an ordinary resolver and yields none, which is
// what keeps the primary service - and its plain SearchDomains, which are a
// different thing - out of the scoped table.
func readScopedDict(key string) (servers, domains []string) {
	out, err := scutil("show " + key + "\nquit\n")
	if err != nil || strings.Contains(out, "No such key") {
		return nil, nil
	}
	var curKey string
	inArray := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.Contains(line, ": <array> {"):
			curKey = strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
			inArray = true
		case line == "}":
			inArray = false
		case inArray:
			if p := strings.SplitN(line, ":", 2); len(p) == 2 {
				v := strings.TrimSpace(p[1])
				switch curKey {
				case "ServerAddresses":
					servers = append(servers, v)
				case "SupplementalMatchDomains":
					domains = append(domains, v)
				}
			}
		}
	}
	return servers, domains
}

// listDNSKeys returns every State:/Network/Service/<id>/DNS key the store holds,
// which is where each service, a VPN's included, publishes its resolver.
func listDNSKeys() []string {
	out, err := scutil("list State:/Network/Service/.*/DNS\nquit\n")
	if err != nil {
		return nil
	}
	var keys []string
	for _, line := range strings.Split(out, "\n") {
		if _, key, ok := strings.Cut(line, "= "); ok {
			keys = append(keys, strings.TrimSpace(key))
		}
	}
	return keys
}
