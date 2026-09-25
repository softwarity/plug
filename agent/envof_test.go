package agent

import (
	"reflect"
	"strings"
	"testing"
)

// What a process must inherit from the workload it replaces, and what it must
// not: the application's own variables come through, the container's runtime
// and the cluster's plumbing do not. KUBERNETES_* and the *_SERVICE_HOST pairs
// are addresses that exist only inside the cluster; handed to a process
// outside it they are the first thing to break.
func TestEnvOfKeepsTheApplicationAndDropsThePlumbing(t *testing.T) {
	got := envLines([]string{
		"APP_DB_HOST=odb", "APP_DB_PASSWORD=s3cret", "PORT=8080",
		"PATH=/usr/bin", "HOME=/root", "HOSTNAME=orders-svc-7d9f", "TERM=xterm",
		"KUBERNETES_SERVICE_HOST=10.96.0.1", "KUBERNETES_PORT=tcp://10.96.0.1:443",
		"ODB_SERVICE_HOST=10.96.12.3", "ODB_SERVICE_PORT=5432", "ODB_SERVICE_PORT_PG=5432",
		"PLUG_CORE=1", "JAVA_HOME=/opt/jdk", "NODE_PATH=/app/node_modules",
	})
	want := []string{"APP_DB_HOST=odb", "APP_DB_PASSWORD=s3cret", "PORT=8080"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Values are carried as they are: an "=" inside the value, an empty value, a
// duplicate key where the last wins as execve does. Malformed entries with no
// "=" are dropped rather than invented.
func TestEnvOfCarriesValuesVerbatim(t *testing.T) {
	got := envLines([]string{"DATABASE_URL=postgres://u:p@odb:5432/db?sslmode=require", "EMPTY=", "X=1", "X=2", "garbage"})
	want := []string{"DATABASE_URL=postgres://u:p@odb:5432/db?sslmode=require", "EMPTY=", "X=2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// /proc/<pid>/environ is NUL-separated, trailing NUL included.
func TestProcEnvironSplitsOnNUL(t *testing.T) {
	got := procEnviron([]byte("A=1\x00B=two words\x00"))
	if !reflect.DeepEqual(got, []string{"A=1", "B=two words"}) {
		t.Fatalf("got %v", got)
	}
}

// The service links kube injects, as read off a real pod: sixty lines for a
// namespace of twenty services, every one a ClusterIP that exists nowhere
// else. They go. An application's own PORT=3000 stays, and so does a FOO_PORT
// whose value is a number: only the tcp:// shape is a link.
func TestKubeServiceLinksAreDroppedAndTheAppsOwnPortStays(t *testing.T) {
	got := envLines([]string{
		"AERO_CLIM_SVC_PORT=tcp://86.32.143.45:3134", "AERO_CLIM_SVC_PORT_3134_TCP=tcp://86.32.143.45:3134",
		"AERO_CLIM_SVC_PORT_3134_TCP_ADDR=86.32.143.45", "AERO_CLIM_SVC_PORT_3134_TCP_PORT=3134",
		"AERO_CLIM_SVC_PORT_3134_TCP_PROTO=tcp", "AERO_CLIM_SVC_SERVICE_HOST=86.32.143.45",
		"RABBITMQ_PORT_5672_UDP=udp://1.2.3.4:5672",
		"PORT=3000", "APP_SCHEDULER_PORT=3017", "APP_DB_PASSWORD=s3cret",
	})
	want := []string{"APP_DB_PASSWORD=s3cret", "APP_SCHEDULER_PORT=3017", "PORT=3000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The wire format the fix turns on: env-ofz frames records with NUL, so a
// value that contains a newline (a PEM) crosses whole, notes and all; env-of
// keeps the newline form, where that same value would be cut at its first line.
func TestFormatEnvReplyNULCarriesNewlines(t *testing.T) {
	pemVal := "-----BEGIN-----\nMIID\n-----END-----"
	entries := []string{"PGPASSWORD=s3cret", "CA=" + pemVal}
	notes := []string{"a note"}

	z := formatEnvReply(entries, notes, true)
	recs := strings.Split(z, "\x00")
	want := []string{"# a note", "PGPASSWORD=s3cret", "CA=" + pemVal}
	if !reflect.DeepEqual(recs, want) {
		t.Fatalf("NUL frame:\n got %q\nwant %q", recs, want)
	}
	// The CA record still holds its two newlines - the whole point.
	if got := recs[2]; got != "CA="+pemVal {
		t.Fatalf("PEM record altered: %q", got)
	}

	// Legacy: newline-separated, notes first. The PEM's newlines are now
	// indistinguishable from separators - the limitation the NUL form removes.
	nl := formatEnvReply(entries, notes, false)
	if nl != "# a note\nPGPASSWORD=s3cret\nCA="+pemVal {
		t.Fatalf("legacy frame: %q", nl)
	}
}
