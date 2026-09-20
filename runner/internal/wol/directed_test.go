package wol

import (
	"net"
	"testing"
)

func TestDirectedBroadcast(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		want    string
		wantErr bool
	}{
		{name: "typical home /24", cidr: "192.168.1.37/24", want: "192.168.1.255"},
		{name: "/16", cidr: "172.17.0.2/16", want: "172.17.255.255"},
		{name: "/8", cidr: "10.1.2.3/8", want: "10.255.255.255"},
		{name: "/25 lower half", cidr: "192.168.1.10/25", want: "192.168.1.127"},
		{name: "/25 upper half", cidr: "192.168.1.200/25", want: "192.168.1.255"},
		{name: "/30 point to point style", cidr: "10.0.0.1/30", want: "10.0.0.3"},
		{name: "/32 host route", cidr: "100.64.0.1/32", want: "100.64.0.1"},
		{name: "already broadcast", cidr: "192.168.1.255/24", want: "192.168.1.255"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip, ipNet, err := net.ParseCIDR(tt.cidr)
			if err != nil {
				t.Fatalf("ParseCIDR(%q): %v", tt.cidr, err)
			}
			got, err := DirectedBroadcast(ip, ipNet.Mask)
			if err != nil {
				t.Fatalf("DirectedBroadcast: %v", err)
			}
			if got.String() != tt.want {
				t.Errorf("DirectedBroadcast(%s) = %s, want %s", tt.cidr, got, tt.want)
			}
		})
	}
}

func TestDirectedBroadcastRejectsIPv6(t *testing.T) {
	ip, ipNet, err := net.ParseCIDR("fe80::1/64")
	if err != nil {
		t.Fatalf("ParseCIDR: %v", err)
	}
	if _, err := DirectedBroadcast(ip, ipNet.Mask); err == nil {
		t.Error("DirectedBroadcast on IPv6 succeeded, want error")
	}
}

// TestDirectedBroadcastAcceptsIPv4In6Mask covers the shape net.Interface
// addresses can hand back: an IPv4 address carrying a 16-byte mask.
func TestDirectedBroadcastAcceptsIPv4In6Mask(t *testing.T) {
	mask := make(net.IPMask, net.IPv6len)
	copy(mask[12:], net.CIDRMask(24, 32))
	got, err := DirectedBroadcast(net.ParseIP("192.168.5.9"), mask)
	if err != nil {
		t.Fatalf("DirectedBroadcast: %v", err)
	}
	if got.String() != "192.168.5.255" {
		t.Errorf("got %s, want 192.168.5.255", got)
	}
}

func TestWakePortsIncludes9And7(t *testing.T) {
	want := map[int]bool{9: false, 7: false}
	for _, p := range WakePorts {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("WakePorts missing port %d", p)
		}
	}
}

// TestInterfaceSenderIsAPacketSender pins the injectability contract that
// wakerd's tests rely on.
func TestInterfaceSenderIsAPacketSender(t *testing.T) {
	var _ PacketSender = InterfaceSender{}
	var _ PacketSender = DirectedSender
}
