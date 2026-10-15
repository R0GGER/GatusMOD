package probe

import (
	"strings"
	"testing"
)

func TestEvaluate(t *testing.T) {
	known := map[string]bool{"CONNECTED": true, "OFFSET": true, "STATE": true, "VALUE": true, "BANDWIDTH": true, "CERTIFICATE_EXPIRATION": true}
	values := Values{"CONNECTED": true, "OFFSET": 812.5, "STATE": "busy", "VALUE": "Linux router 5.10", "CERTIFICATE_EXPIRATION": 3600000.0}
	cases := map[string]bool{
		"[CONNECTED] == true":                   true,
		"[connected] == TRUE":                   true,
		"[OFFSET] < 500ms":                      false,
		"[OFFSET] <= 1s":                        true,
		"[OFFSET] > 800":                        true,
		"[STATE] == ready":                      false,
		"[STATE] != ready":                      true,
		"[VALUE] == pat(Linux*)":                true,
		"[VALUE] != pat(*BSD*)":                 true,
		`[VALUE] == "Linux router 5.10"`:        true,
		"len([VALUE]) > 5":                      true,
		"[CERTIFICATE_EXPIRATION] > 48h":        false,
		"[CERTIFICATE_EXPIRATION] >= 30m":       true,
		"[CERTIFICATE_EXPIRATION] == 1h":        true,
		"[BANDWIDTH] > 100":                     true, // not measured yet: skipped
		"[STATE] < ready":                       false,
		"[OFFSET] != 812.5":                     false,
		"[CONNECTED] == true && something else": false,
		"[VALUE] == pat(router)":                false,
		"[VALUE] == Linux router 5.10":          true,
	}
	for cond, want := range cases {
		failed := evaluate([]string{cond}, values, known)
		if got := len(failed) == 0; got != want {
			t.Errorf("%s: got %v (%v), want %v", cond, got, failed, want)
		}
	}
}

func TestEvaluate_Errors(t *testing.T) {
	known := map[string]bool{"CONNECTED": true, "OFFSET": true}
	failed := evaluate([]string{"[STATUS] == 200", "no operator", "[OFFSET] < 500ms"}, Values{"CONNECTED": true, "OFFSET": 812.0}, known)
	if len(failed) != 3 {
		t.Fatalf("expected 3 failures, got %v", failed)
	}
	if !strings.Contains(failed[0], "unknown placeholder [STATUS]") || !strings.Contains(failed[1], "no operator") {
		t.Errorf("unexpected messages %v", failed)
	}
	if failed[2] != "[OFFSET] (812) < 500ms" {
		t.Errorf("unexpected description %q", failed[2])
	}
	if _, err := parseCondition("[OFFSET] < pat(1*)", known); err == nil {
		t.Error("pat() with < must be rejected")
	}
}

func TestWildcard(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"*", "", true}, {"a*", "abc", true}, {"*c", "abc", true}, {"a*c", "abc", true}, {"a*b*c", "aXbYc", true},
		{"a*c", "ab", false}, {"abc", "abc", true}, {"ab", "abc", false}, {"a*a", "a", false},
	}
	for _, c := range cases {
		if got := wildcard(c.pattern, c.s); got != c.want {
			t.Errorf("wildcard(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}
