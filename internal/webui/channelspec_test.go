//go:build webui

package webui

import (
	"errors"
	"testing"
)

// The WebUI must not offer the "log" channel type: its output goes to the
// server log that the account owner cannot read, so selecting it looks like a
// silent delivery failure. The base build still supports type: log through
// config.yaml; these tests only pin the WebUI channel catalogue.
func TestLogChannelIsNotOfferedInWebUI(t *testing.T) {
	types := ChannelTypes()
	for _, typ := range types {
		if typ == "log" {
			t.Fatalf("ChannelTypes() still offers log: %v", types)
		}
	}
	if _, ok := ChannelFields["log"]; ok {
		t.Fatal("ChannelFields still declares log")
	}
	if _, ok := FieldsOf("log"); ok {
		t.Fatal(`FieldsOf("log") should report unsupported`)
	}

	_, err := BuildSpec("log", "log", nil)
	var ce *ChannelError
	if !errors.As(err, &ce) || ce.Code != "invalid_channel_type" {
		t.Fatalf(`BuildSpec("log") error = %v, want invalid_channel_type`, err)
	}
}

// Guard against removing too much: the real delivery providers must remain.
func TestRealChannelTypesRemainOffered(t *testing.T) {
	types := ChannelTypes()
	seen := make(map[string]bool, len(types))
	for _, typ := range types {
		seen[typ] = true
	}
	for _, must := range []string{"email", "telegram", "wecom", "webhook"} {
		if !seen[must] {
			t.Fatalf("ChannelTypes() = %v, missing %q", types, must)
		}
	}
}
