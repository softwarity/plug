package agent

import (
	"reflect"
	"testing"
)

// What a process must inherit from the workload it replaces, and what it must
// not: the application's own variables come through, the container's runtime
// and the cluster's plumbing do not. KUBERNETES_* and the *_SERVICE_HOST pairs
// are addresses that exist only inside the cluster; handed to a process
// outside it they are the first thing to break.
func TestEnvOfKeepsTheApplicationAndDropsThePlumbing(t *testing.T) {
	got := envLines([]string{
		"NEO_ODB_HOST=odb", "NEO_ODB_PASSWORD=s3cret", "PORT=8080",
		"PATH=/usr/bin", "HOME=/root", "HOSTNAME=fpl-svc-7d9f", "TERM=xterm",
		"KUBERNETES_SERVICE_HOST=10.96.0.1", "KUBERNETES_PORT=tcp://10.96.0.1:443",
		"ODB_SERVICE_HOST=10.96.12.3", "ODB_SERVICE_PORT=5432", "ODB_SERVICE_PORT_PG=5432",
		"PLUG_CORE=1", "JAVA_HOME=/opt/jdk", "NODE_PATH=/app/node_modules",
	})
	want := []string{"NEO_ODB_HOST=odb", "NEO_ODB_PASSWORD=s3cret", "PORT=8080"}
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
