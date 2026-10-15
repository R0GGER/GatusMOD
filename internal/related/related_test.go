package related

import (
	"testing"

	"gatusmod/internal/badge"
)

func TestKey(t *testing.T) {
	cases := map[[2]string]string{
		{"core", "example"}:              "core_example",
		{"Search Engines", "Google.com"}: "search-engines_google-com",
		{"a/b_c", "x,y#z+w&v"}:           "a-b-c_x-y-z-w-v",
		{"", "Solo"}:                     "_solo",
		{" Core ", " Thing "}:            "core_thing",
	}
	for in, want := range cases {
		if got := badge.Key(in[0], in[1]); got != want {
			t.Errorf("Key(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

const sample = `
endpoints:
  - name: web
    group: servers
    url: https://Host.Example.org/health
  - name: tcp
    group: servers
    url: tcp://host.example.org:5201
  - name: ping other group
    group: other
    url: icmp://host.example.org
  - name: ping
    group: servers
    url: icmp://host.example.org
  - name: lonely
    group: servers
    url: https://lonely.example.org
external-endpoints:
  - name: iperf
    group: servers
    token: x
  - name: backup job
    group: servers
    token: x
`

func TestFind(t *testing.T) {
	eps := parseEndpoints([]byte(sample), map[string]string{"servers_iperf": "iperf3://Host.example.org:5201", "servers_backup-job": "push://"})
	resp, ok := Find(eps, "servers_iperf")
	if !ok || resp.Type != "iperf3" || resp.Host != "host.example.org" || resp.ICMP == nil || *resp.ICMP != "servers_ping" {
		t.Fatalf("iperf3 external endpoint: got %+v %v", resp, ok)
	}
	if resp, ok = Find(eps, "servers_backup-job"); !ok || resp.Type != "push" || resp.Host != "" || resp.ICMP != nil {
		t.Fatalf("push external endpoint: got %+v %v", resp, ok)
	}
	resp, ok = Find(eps, "servers_web")
	if !ok || resp.Type != "http" || resp.Host != "host.example.org" || resp.ICMP == nil || *resp.ICMP != "servers_ping" {
		t.Fatalf("web: got %+v %v", resp, ok)
	}
	resp, ok = Find(eps, "servers_tcp")
	if !ok || resp.Type != "tcp" || resp.ICMP == nil || *resp.ICMP != "servers_ping" {
		t.Fatalf("tcp: got %+v %v", resp, ok)
	}
	if resp, ok = Find(eps, "servers_lonely"); !ok || resp.ICMP != nil {
		t.Fatalf("lonely: got %+v %v", resp, ok)
	}
	if resp, ok = Find(eps, "servers_ping"); !ok || resp.Type != "icmp" || resp.ICMP != nil {
		t.Fatalf("icmp itself: got %+v %v", resp, ok)
	}
	if _, ok = Find(eps, "unknown_key"); ok {
		t.Fatal("unknown key should not be found")
	}
}
