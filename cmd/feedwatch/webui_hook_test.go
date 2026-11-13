//go:build !webui

package main

import "testing"

// The base build's hook is a compile-time no-op. Guarding it here (rather than
// in main_test.go) keeps `go test -tags webui ./...` from accidentally booting
// a real HTTP server through the test suite.
func TestWebUILaunchIsAConstantFalseStub(t *testing.T) {
	handled, err := webuiLaunch("config.yaml", "info", "text", false, false)
	if err != nil {
		t.Fatalf("stub returned an error: %v", err)
	}
	if handled {
		t.Fatal("the base build must never hand control to the webui variant")
	}
}
