package main

// dockerHostAddr is how the docker daemon's kernel reaches THIS host: on
// Linux the daemon runs on it, so the loopback the forward listens on.
func dockerHostAddr() string { return "127.0.0.1" }
