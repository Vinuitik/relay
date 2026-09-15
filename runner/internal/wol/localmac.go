package wol

import (
	"fmt"
	"net"
	"strings"
)

// virtualIfacePrefixes are interface name prefixes that are never the real
// NIC a WoL magic packet needs to target - Tailscale's own interface,
// Docker bridges/veth pairs, loopback, and common VPN/tunnel adapters.
// Heuristic, not exhaustive: a machine with an unusual NIC naming scheme
// (or, e.g., WoL over WiFi rather than Ethernet, which many WiFi chipsets
// don't support at all regardless of what MAC gets embedded) may need
// RELAY_WOL_MAC set manually instead - see runner/FLOWS.md "Wake-on-LAN".
var virtualIfacePrefixes = []string{
	"lo", "tailscale", "docker", "veth", "br-", "virbr", "vmnet", "tun", "tap", "utun", "zt",
}

// LocalMAC picks this machine's own real (non-virtual) network interface's
// hardware address, for embedding in the pairing QR code (see
// cmd/runnerd/main.go's printPairingQR) - the exact value a KnownRunner's
// wakeMac needs to be woken via another runner's /v1/wake. Picks the first
// up, non-virtual interface that has an assigned IP address (a heuristic
// for "this is the NIC actually connected to the LAN", not guaranteed
// correct on a multi-NIC machine). Returns an error if none is found -
// callers should treat that as "omit the MAC, fall back to manual entry",
// not a fatal condition.
func LocalMAC() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("list network interfaces: %w", err)
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		if isVirtualIfaceName(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}
		return iface.HardwareAddr.String(), nil
	}
	return "", fmt.Errorf("no non-virtual network interface with an assigned address found")
}

func isVirtualIfaceName(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range virtualIfacePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
