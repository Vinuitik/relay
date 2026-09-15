package wol

import "testing"

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
