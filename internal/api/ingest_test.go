package api

import (
	"net"
	"testing"
)

// isForbiddenIP: the SSRF blocklist surface — every range PLAN.md names.
func TestIsForbiddenIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "0.0.0.0", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"100.64.0.1", "192.0.2.1", "198.51.100.7", "203.0.113.9",
		"::1", "fc00::1", "fe80::1", "ff02::1", "2001:db8::1",
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test ip %s", s)
		}
		if !isForbiddenIP(ip) {
			t.Fatalf("%s must be blocked", s)
		}
	}
	allowed := []string{
		"8.8.8.8", "1.1.1.1", "172.32.0.1", "100.128.0.1", // outside CGNAT
		"2606:4700::1", "11.0.0.1", "198.51.101.1", "203.0.114.1",
	}
	for _, s := range allowed {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test ip %s", s)
		}
		if isForbiddenIP(ip) {
			t.Fatalf("%s must be allowed", s)
		}
	}
}
