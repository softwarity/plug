package main

import "testing"

// `plug mounts` shows the mounts of sessions that are alive, automatic ones
// included (they are the ones nobody asked for and everybody needs to find),
// and leaves a dead session's leftovers to doctor.
func TestMountsListsLiveSessionsOnly(t *testing.T) {
	records := []mountRecord{
		{pid: 10, path: "/tmp/s1/data", spec: "api:/data:/tmp/s1/data", cluster: "k8s:32222", auto: true},
		{pid: 20, path: "/home/me/srv", spec: "api:data:/home/me/srv", cluster: "k8s:32222"},
		{pid: 30, path: "/tmp/dead", spec: "web:/x:/tmp/dead", cluster: "k8s:32222", auto: true},
		{pid: 0, path: "/tmp/none", spec: "web:/y:/tmp/none"},
	}
	alive := func(pid int) bool { return pid == 10 || pid == 20 }
	mounted := func(path string) bool { return path != "/home/me/srv" }
	rows := liveMounts(records, alive, mounted)
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	if rows[0].path != "/home/me/srv" || rows[0].volume != "api:data" || rows[0].session != "pid 20, --mount" || rows[0].state == "mounted" {
		t.Fatalf("explicit row: %+v", rows[0])
	}
	if rows[1].path != "/tmp/s1/data" || rows[1].volume != "api:/data" || rows[1].session != "pid 10, automatic" || rows[1].state != "mounted" || rows[1].cluster != "k8s:32222" {
		t.Fatalf("automatic row: %+v", rows[1])
	}
}

// A record written by an older plug has no cluster line; the column says so
// rather than printing nothing.
func TestMountsWordsAMissingCluster(t *testing.T) {
	rows := liveMounts([]mountRecord{{pid: 1, path: "/p", spec: "a:v:/p"}}, func(int) bool { return true }, func(string) bool { return true })
	if len(rows) != 1 || rows[0].cluster != "?" {
		t.Fatalf("%+v", rows)
	}
}
