package probe

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIPerf3_Handshake(t *testing.T) {
	port := fakeIPerf3(t, false)
	res := Check(context.Background(), "iperf3://127.0.0.1:"+port, []string{"[CONNECTED] == true", "[STATE] == ready"}, Options{})
	if !res.Success || res.Values["STATE"] != "ready" || res.Values["PORT"] != port {
		t.Fatalf("unexpected result %+v", res)
	}
	if _, ok := res.Values["BANDWIDTH"]; ok {
		t.Error("a handshake must not measure the bandwidth")
	}
}

func TestIPerf3_BusyTriesNextPort(t *testing.T) {
	busy := fakeIPerf3(t, true)
	res := Check(context.Background(), "iperf3://127.0.0.1:"+busy, []string{"[CONNECTED] == true"}, Options{})
	if !res.Success || res.Values["STATE"] != "busy" {
		t.Fatalf("a busy server is reachable: %+v", res)
	}
	if res := Check(context.Background(), "iperf3://127.0.0.1:"+busy, []string{"[STATE] == ready"}, Options{}); res.Success {
		t.Fatal("[STATE] == ready must fail for a busy server")
	}
	ready := fakeIPerf3(t, false)
	res = Check(context.Background(), "iperf3://127.0.0.1:"+busy+"?ports="+busy+","+closedPort(t)+","+ready, []string{"[STATE] == ready"}, Options{})
	if !res.Success || res.Values["PORT"] != ready {
		t.Fatalf("expected the ready port %s: %+v", ready, res)
	}
}

func TestIPerf3_Down(t *testing.T) {
	res := Check(context.Background(), "iperf3://127.0.0.1:"+closedPort(t), []string{"[CONNECTED] == true"}, Options{})
	if res.Success || res.Values["CONNECTED"] != false || res.Error == "" {
		t.Fatalf("expected down: %+v", res)
	}
}

func TestIPerf3_Bandwidth(t *testing.T) {
	port := fakeIPerf3(t, false)
	raw := "iperf3://127.0.0.1:" + port + "?bandwidth=1s"
	if every, ok := BandwidthInterval(raw); !ok || every != 0 {
		t.Errorf("without bandwidth-interval the test runs on every check, got %v %v", every, ok)
	}
	if every, ok := BandwidthInterval(raw + "&bandwidth-interval=6h"); !ok || every != 6*time.Hour {
		t.Errorf("bandwidth-interval=6h: got %v %v", every, ok)
	}
	if _, ok := BandwidthInterval("iperf3://127.0.0.1:" + port); ok {
		t.Error("a handshake has no data test")
	}
	res := Check(context.Background(), raw, []string{"[BANDWIDTH] > 1"}, Options{Bandwidth: true})
	bw, _ := res.Values["BANDWIDTH"].(float64)
	if !res.Success || bw <= 1 {
		t.Fatalf("expected a bandwidth result: %+v", res)
	}
	prev := Check(context.Background(), raw, []string{"[BANDWIDTH] > 1"}, Options{Previous: Values{"BANDWIDTH": 0.5}})
	if prev.Success || !strings.Contains(prev.Error, "[BANDWIDTH] (0.5) > 1") {
		t.Fatalf("the previous bandwidth must be evaluated between tests: %+v", prev)
	}
}

func TestIPerf3_RateLimited(t *testing.T) {
	port := fakeIPerf3(t, false)
	raw := "iperf3://127.0.0.1:" + port + "?bandwidth=2s&rate=1M&ip=4"
	res := Check(context.Background(), raw, []string{"[CONNECTED] == true"}, Options{Bandwidth: true})
	bw, _ := res.Values["BANDWIDTH"].(float64)
	if !res.Success || bw < 0.8 || bw > 1.2 {
		t.Fatalf("expected about 1 Mbit/s: %+v", res)
	}
}

func TestParseRate(t *testing.T) {
	for in, want := range map[string]float64{"1M": 1e6, "500k": 5e5, "1.5G": 1.5e9, "20000": 2e4} {
		if got, err := parseRate(in); err != nil || got != want {
			t.Errorf("%s: got %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "fast", "-1M", "10", "M"} {
		if _, err := parseRate(in); err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
	if got := FormatRate(1e6); got != "1 Mbit/s" {
		t.Errorf("FormatRate: %s", got)
	}
}

func TestNTP(t *testing.T) {
	port := fakeNTP(t)
	res := Check(context.Background(), "ntp://127.0.0.1:"+port+"?max-offset=1s", []string{"[CONNECTED] == true", "[STRATUM] <= 3"}, Options{})
	if !res.Success || res.Values["STRATUM"] != 2 {
		t.Fatalf("unexpected result %+v", res)
	}
	if off, _ := res.Values["OFFSET"].(float64); off > 1000 {
		t.Errorf("offset %v ms is too large for a local fake", off)
	}
	res = Check(context.Background(), "ntp://127.0.0.1:"+port+"?max-offset=1ns", []string{"[CONNECTED] == true"}, Options{})
	if res.Success && res.Values["OFFSET"].(float64) > 0 {
		t.Fatalf("max-offset must add an offset condition: %+v", res)
	}
}

func TestSMTP(t *testing.T) {
	port := fakeSMTP(t, "220 fake ESMTP ready")
	res := Check(context.Background(), "smtp://127.0.0.1:"+port, []string{"[CONNECTED] == true", "[CODE] == 220"}, Options{})
	if !res.Success || res.Values["CODE"] != 220 {
		t.Fatalf("unexpected result %+v", res)
	}
	res = Check(context.Background(), "smtp://127.0.0.1:"+port+"?starttls=true", []string{"[CONNECTED] == true"}, Options{})
	if res.Success || !strings.Contains(res.Error, "STARTTLS") {
		t.Fatalf("STARTTLS is required but not offered: %+v", res)
	}
	port = fakeSMTP(t, "554 no service")
	res = Check(context.Background(), "smtp://127.0.0.1:"+port, []string{"[CONNECTED] == true", "[CODE] == 220"}, Options{})
	if res.Success || res.Values["CODE"] != 554 || !strings.Contains(res.Error, "[CODE] (554) == 220") {
		t.Fatalf("a 554 banner must fail [CODE] == 220: %+v", res)
	}
}

func TestValidate(t *testing.T) {
	ok := map[string][]string{
		"iperf3://h:5201?ports=5201-5210&bandwidth=3s&bandwidth-interval=6h": {"[STATE] == ready", "[BANDWIDTH] > 100"},
		"iperf3://h?bandwidth=5s&rate=1M&ip=4&reverse=true":                  {"[CONNECTED] == true"},
		"ntp://pool.ntp.org":                       {"[OFFSET] < 100ms"},
		"snmp://public@h/1.3.6.1.2.1.1.5.0":        {"[VALUE] == pat(*router*)"},
		"smtps://mail.example.org":                 {"[CERTIFICATE_EXPIRATION] > 48h"},
		"postgres://u:p@db/app?sslmode=disable":    {"[CONNECTED] == true"},
		"mysql://u:p@db/app?query=SELECT%201":      {"[RESULT] == 1"},
		"push://":                                  nil,
		"redis://:${REDIS_PASSWORD}@cache:6379/0":  {"[CONNECTED] == true"},
		"mongodb+srv://u:p@cluster.example.org/db": {"[CONNECTED] == true"},
	}
	for raw, conds := range ok {
		if err := Validate(raw, conds); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	bad := map[string][]string{
		"ntp://pool.ntp.org":              {"[STATUS] == 200"},
		"ntp://pool.ntp.org?timeout=soon": {"[CONNECTED] == true"},
		"iperf3://h?bandwidth=10m":        {"[CONNECTED] == true"},
		"iperf3://h?rate=fast":            {"[CONNECTED] == true"},
		"iperf3://h?ip=5":                 {"[CONNECTED] == true"},
		"iperf3://h?reverse=maybe":        {"[CONNECTED] == true"},
		"ftp://h":                         {"[CONNECTED] == true"},
		"snmp://":                         {"[CONNECTED] == true"},
		"smtp://mail.example.org":         nil,
	}
	for raw, conds := range bad {
		if err := Validate(raw, conds); err == nil {
			t.Errorf("%s %v: expected an error", raw, conds)
		}
	}
}

func TestIPerfPorts(t *testing.T) {
	u, _ := url.Parse("iperf3://h:5203?ports=5201-5204,5210")
	ports, err := iperfPorts(u)
	if err != nil || strings.Join(ports, ",") != "5203,5201,5202,5204,5210" {
		t.Fatalf("got %v %v", ports, err)
	}
	u, _ = url.Parse("iperf3://h?ports=5201-9999")
	if _, err := iperfPorts(u); err == nil {
		t.Error("a range of more than 100 ports must be rejected")
	}
}

func TestRedact(t *testing.T) {
	got := Redact("postgres://user:s3cret@db:5432/app?sslmode=disable&auth-pass=hunter2")
	if strings.Contains(got, "s3cret") || strings.Contains(got, "hunter2") || !strings.Contains(got, "user:xxxxx@db") {
		t.Errorf("not redacted: %s", got)
	}
	if got := Redact("push://"); got != "push://" {
		t.Errorf("URLs without secrets must stay as they are, got %s", got)
	}
	u, _ := url.Parse("mysql://root:s3cret@db/app")
	if msg := redactError("access denied for root:s3cret", u); strings.Contains(msg, "s3cret") {
		t.Errorf("error not redacted: %s", msg)
	}
	t.Setenv("PROBE_TEST_PASSWORD", "fromenv")
	u, err := Parse("redis://:${PROBE_TEST_PASSWORD}@cache:6379")
	if p, _ := u.User.Password(); err != nil || p != "fromenv" {
		t.Errorf("${VAR} not expanded: %v %v", u, err)
	}
	u, _ = Parse("redis://:pa$word@cache:6379")
	if p, _ := u.User.Password(); p != "pa$word" {
		t.Errorf("a $ without braces must stay: %q", p)
	}
}

func TestDriverURLs(t *testing.T) {
	u, _ := url.Parse("mysql://app:p%40ss@db.example.org/shop?tls=skip-verify&query=SELECT%201&timeout=5s")
	cfg, err := mysqlConfig(driverURL(u))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "app" || cfg.Passwd != "p@ss" || cfg.Addr != "db.example.org:3306" || cfg.DBName != "shop" || cfg.TLSConfig != "skip-verify" {
		t.Errorf("unexpected mysql config %+v", cfg)
	}
	if q := driverURL(u).Query(); q.Has("query") || q.Has("timeout") || q.Get("tls") != "skip-verify" {
		t.Errorf("GatusMOD parameters must be removed: %v", q)
	}
}

func TestDatabase_Down(t *testing.T) {
	port := closedPort(t)
	for _, raw := range []string{
		"postgres://u:secretpw@127.0.0.1:" + port + "/db?connect_timeout=2",
		"mysql://u:secretpw@127.0.0.1:" + port + "/db",
		"redis://:secretpw@127.0.0.1:" + port,
		"mongodb://u:secretpw@127.0.0.1:" + port + "/?connectTimeoutMS=1000&timeout=2s",
	} {
		res := Check(context.Background(), raw, []string{"[CONNECTED] == true"}, Options{})
		if res.Success || res.Values["CONNECTED"] != false {
			t.Errorf("%s: expected down, got %+v", raw, res)
		}
		if strings.Contains(res.Error, "secretpw") {
			t.Errorf("%s: password in error %q", raw, res.Error)
		}
	}
}
