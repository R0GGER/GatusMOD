package mapping

import (
	"strings"
	"testing"

	"gatusmod/internal/config"
	"gatusmod/internal/store"
)

func TestRecord_Templates(t *testing.T) {
	cases := map[string]struct {
		imp  config.Import
		row  store.Row
		want store.Record
	}{
		"tcp with port range": {
			config.Import{ID: "i", Name: "servers", Group: "iperf", Type: config.TypeTCP, Fields: config.Fields{Host: "Host", Port: "Ports"}, Interval: "15m"},
			store.Row{"id": 7.0, "Host": "speed.example.org", "Ports": "5201-5210"},
			store.Record{ID: "7", Name: "speed.example.org", Group: "iperf", URL: "tcp://speed.example.org:5201", Interval: "15m", Conditions: "[CONNECTED] == true"},
		},
		"tcp with default ports and name template": {
			config.Import{Type: config.TypeTCP, Fields: config.Fields{Host: "Host", Port: "Ports"}, DefaultPorts: "5201, 5202", NameTemplate: "{host}:{port} ({Country})"},
			store.Row{"Host": "a.example.org", "Country": map[string]any{"id": 1.0, "value": "NL"}},
			store.Record{Name: "a.example.org:5201 (NL)", URL: "tcp://a.example.org:5201", Conditions: "[CONNECTED] == true"},
		},
		"http default template": {
			config.Import{Type: config.TypeHTTP, Fields: config.Fields{Host: "host", Group: "group"}, Group: "fallback"},
			store.Row{"host": "example.org", "group": "web"},
			store.Record{Name: "example.org", Group: "web", URL: "https://example.org", Conditions: "[STATUS] == 200"},
		},
		"icmp with custom conditions": {
			config.Import{Type: config.TypeICMP, Fields: config.Fields{Host: "host"}, Conditions: "[CONNECTED] == true\n[RESPONSE_TIME] < 200"},
			store.Row{"host": "1.1.1.1"},
			store.Record{Name: "1.1.1.1", URL: "icmp://1.1.1.1", Conditions: "[CONNECTED] == true\n[RESPONSE_TIME] < 200"},
		},
		"url column wins over the template": {
			config.Import{Type: config.TypeHTTP, Fields: config.Fields{Name: "name", URL: "url", Host: "host", Conditions: "cond", Disabled: "off"}},
			store.Row{"name": "x", "url": "https://x.example.org/health", "host": "ignored", "cond": "[STATUS] < 500", "off": true},
			store.Record{Name: "x", URL: "https://x.example.org/health", Conditions: "[STATUS] < 500", Disabled: true},
		},
		"iperf3 with a port range": {
			config.Import{Type: config.TypeIPerf3, Fields: config.Fields{Host: "Host", Port: "Ports"}},
			store.Row{"Host": "speed.example.org", "Ports": "5201 - 5210"},
			store.Record{Name: "speed.example.org", URL: "iperf3://speed.example.org:5201?ports=5201-5210", Conditions: "[CONNECTED] == true"},
		},
		"iperf3 without a port uses 5201": {
			config.Import{Type: config.TypeIPerf3, Fields: config.Fields{Host: "Host"}},
			store.Row{"Host": "speed.example.org"},
			store.Record{Name: "speed.example.org", URL: "iperf3://speed.example.org:5201", Conditions: "[CONNECTED] == true"},
		},
		"smtp defaults": {
			config.Import{Type: config.TypeSMTP, Fields: config.Fields{Host: "host"}},
			store.Row{"host": "mx.example.org"},
			store.Record{Name: "mx.example.org", URL: "smtp://mx.example.org:25", Conditions: "[CONNECTED] == true\n[CODE] == 220"},
		},
		"custom import uses the defaults of the url scheme": {
			config.Import{Type: config.TypeCustom, Fields: config.Fields{Name: "name", URL: "url"}},
			store.Row{"name": "clock", "url": "ntp://pool.ntp.org"},
			store.Record{Name: "clock", URL: "ntp://pool.ntp.org", Conditions: "[CONNECTED] == true"},
		},
		"push has no conditions": {
			config.Import{Type: config.TypePush, Fields: config.Fields{Name: "name"}},
			store.Row{"name": "nightly backup"},
			store.Record{Name: "nightly backup", URL: "push://"},
		},
		"conditions in extra are kept": {
			config.Import{Type: config.TypeCustom, Fields: config.Fields{Name: "name", URL: "url", Extra: "extra"}},
			store.Row{"name": "dns", "url": "8.8.8.8", "extra": "conditions:\n  - \"[DNS_RCODE] == NOERROR\"\n"},
			store.Record{Name: "dns", URL: "8.8.8.8", Extra: "conditions:\n  - \"[DNS_RCODE] == NOERROR\"\n"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Record(&tc.imp, tc.row, "id")
			if err != nil {
				t.Fatal(err)
			}
			tc.want.Source, tc.want.SourceName = tc.imp.ID, tc.imp.Name
			if got != tc.want {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestRecords_Problems(t *testing.T) {
	imp := &config.Import{Type: config.TypeTCP, Fields: config.Fields{Host: "host", Port: "port"}}
	rows := []store.Row{
		{"Id": 1.0, "host": "ok.example.org", "port": 80.0},
		{"Id": 2.0, "host": "no-port.example.org"},
		{"Id": 3.0},
		{"Id": 4.0, "host": "bad-port.example.org", "port": "99999"},
	}
	records, problems := Records(imp, rows, "Id")
	if len(records) != 1 || records[0].URL != "tcp://ok.example.org:80" {
		t.Errorf("unexpected records %+v", records)
	}
	if len(problems) != 3 || problems[0].Row != "row id 2" || !strings.Contains(problems[0].Message, "{port}") {
		t.Errorf("unexpected problems %+v", problems)
	}
}

func TestFirstPort(t *testing.T) {
	for in, want := range map[string]string{"5201": "5201", "5201-5210": "5201", " 80, 443": "80", "": "", "abc": "", "0": "", "70000": ""} {
		if got := firstPort(in); got != want {
			t.Errorf("firstPort(%q) = %q, want %q", in, got, want)
		}
	}
}
