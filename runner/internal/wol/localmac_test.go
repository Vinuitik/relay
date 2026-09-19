package wol

import (
	"net"
	"testing"
)

func TestIsVirtualIfaceName(t *testing.T) {
	cases := map[string]bool{
		"eth0":       false,
		"wlan0":      false,
		"enp3s0":     false,
		"Ethernet":   false,
		"lo":         true,
		"tailscale0": true,
		"docker0":    true,
		"veth1234":   true,
		"br-abcdef":  true,
		"tun0":       true,
	}
	for name, wantVirtual := range cases {
		if got := isVirtualIfaceName(name); got != wantVirtual {
			t.Errorf("isVirtualIfaceName(%q) = %v, want %v", name, got, wantVirtual)
		}
	}
}

// TestFirstIPv4AddrSkipsLeadingIPv6 guards against the real bug found on
// Windows (2026-09-19): an interface's Addrs() commonly lists its IPv6
// link-local address before its IPv4 one, which silently broke LocalSubnet
// for every caller on that OS when the code just took addrs[0].
func TestFirstIPv4AddrSkipsLeadingIPv6(t *testing.T) {
	_, ipv6Net, _ := net.ParseCIDR("fe80::1/64")
	_, ipv4Net, _ := net.ParseCIDR("192.168.1.40/24")
	addrs := []net.Addr{ipv6Net, ipv4Net}

	got := firstIPv4Addr(addrs)
	if got == nil {
		t.Fatal("firstIPv4Addr returned nil, want the IPv4 address")
	}
	ipNet, ok := got.(*net.IPNet)
	if !ok || ipNet.IP.To4() == nil {
		t.Errorf("firstIPv4Addr returned %v, want an IPv4 *net.IPNet", got)
	}
}

func TestFirstIPv4AddrNoneFound(t *testing.T) {
	_, ipv6Net, _ := net.ParseCIDR("fe80::1/64")
	if got := firstIPv4Addr([]net.Addr{ipv6Net}); got != nil {
		t.Errorf("firstIPv4Addr = %v, want nil when no IPv4 address exists", got)
	}
}
