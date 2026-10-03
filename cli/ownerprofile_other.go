//go:build !windows

package main

func guardKeyOwner(_, _ string) {}

func keyOwnerError(_, _ string) error { return nil }
