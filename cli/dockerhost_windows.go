package main

// dockerHostAddr is how the docker daemon's kernel reaches THIS host: on
// Docker Desktop, the VM's name for its host.
func dockerHostAddr() string { return "host.docker.internal" }
