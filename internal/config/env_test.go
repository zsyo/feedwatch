package config

import (
	"strings"
	"testing"
)

func TestExpandEnvRefs(t *testing.T) {
	t.Setenv("FW_TEST_TOKEN", "tok_123")
	t.Setenv("FW_TEST_EMPTY", "")
	in := map[string]string{
		"webhook_url": "https://x.test/hook?key=${FW_TEST_TOKEN}",
		"body":        `{"schema": "$schema", "token": "${FW_TEST_TOKEN}"}`,
		"missing":     "${FW_TEST_MISSING}/${FW_TEST_EMPTY}/${FW_TEST_MISSING}",
		"literal":     "$schema $FW_TEST_TOKEN ${not a var}",
	}
	out, missing := ExpandEnvRefs(in)
	if out["webhook_url"] != "https://x.test/hook?key=tok_123" {
		t.Errorf("webhook_url = %q", out["webhook_url"])
	}
	if !strings.Contains(out["body"], `"$schema"`) {
		t.Errorf("bare $NAME must not be expanded: %q", out["body"])
	}
	if out["literal"] != "$schema $FW_TEST_TOKEN ${not a var}" {
		t.Errorf("literal was modified: %q", out["literal"])
	}
	want := []string{"FW_TEST_EMPTY", "FW_TEST_MISSING"}
	if strings.Join(missing, ",") != strings.Join(want, ",") {
		t.Errorf("missing = %v, want %v (sorted, deduplicated)", missing, want)
	}
}

func TestExpandEnvRefsNilIsSafe(t *testing.T) {
	out, missing := ExpandEnvRefs(nil)
	if len(out) != 0 || len(missing) != 0 {
		t.Errorf("nil input should give empty results, got %v / %v", out, missing)
	}
}
