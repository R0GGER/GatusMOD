package config

import "testing"

func TestSchemeType(t *testing.T) {
	cases := map[string]string{
		"iperf3://h:5201":         TypeIPerf3,
		"SMTPS://mail":            TypeSMTP,
		"postgresql://u@db/app":   TypeDatabase,
		"mongodb+srv://c/db":      TypeDatabase,
		"push://":                 TypePush,
		"https://example.org":     "",
		"tcp://h:22":              "",
		"8.8.8.8":                 "",
		"://missing-scheme":       "",
		"rediss://:pw@cache:6380": TypeDatabase,
	}
	for in, want := range cases {
		if got := SchemeType(in); got != want {
			t.Errorf("SchemeType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTokens(t *testing.T) {
	a := &Config{ProbeSecret: "one"}
	b := &Config{ProbeSecret: "two"}
	if a.GatusToken() == b.GatusToken() || len(a.GatusToken()) != 64 {
		t.Error("the Gatus token must depend on the secret")
	}
	if a.PushToken("g_x") == a.PushToken("g_y") || len(a.PushToken("g_x")) != 32 {
		t.Error("push tokens must differ per endpoint")
	}
	if a.PushToken("g_x") != (&Config{ProbeSecret: "one"}).PushToken("g_x") {
		t.Error("push tokens must be stable")
	}
}
