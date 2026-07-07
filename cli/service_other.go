//go:build !windows

package main

// install-service / remove-service exist only on Windows, where the global datapath
// runs in an SCM service. macOS (setuid helper) and Linux (setcap + mount namespace)
// grant privilege differently, so these are no-ops that say so.
func installService() { info("`plug install-service` is Windows-only") }
func removeService()  { info("`plug remove-service` is Windows-only") }
