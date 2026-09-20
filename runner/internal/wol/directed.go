package wol

import (
	"fmt"
	"log"
	"net"
	"strings"
)

// WakePorts are the UDP ports a magic packet is sent to. Port 9 (discard) is
// the convention almost everything uses; port 7 (echo) is the older one some
// NIC firmware still listens on. Sending both costs one extra 102-byte
// datagram per interface and removes a whole class of "the NIC just ignored
// it" failures.
var WakePorts = []int{9, 7}

// DirectedBroadcast returns the directed (subnet) broadcast address for the
// given interface address - the network address with all host bits set, e.g.
// 192.168.1.37/24 -> 192.168.1.255.
//
// This is the address that actually matters for Wake-on-LAN on a multi-homed
// host. Sending to the *limited* broadcast 255.255.255.255 from a socket
// bound to 0.0.0.0 leaves the egress interface up to the routing table, and
// with Tailscale + Docker bridges + WiFi all up the packet routinely leaves
// via an interface the sleeping machine isn't on, with no error reported -
// the wake just silently never happens.
func DirectedBroadcast(ip net.IP, mask net.IPMask) (net.IP, error) {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("directed broadcast: %v is not an IPv4 address", ip)
	}
	if len(mask) == net.IPv6len {
		mask = mask[12:]
	}
	if len(mask) != net.IPv4len {
		return nil, fmt.Errorf("directed broadcast: mask %v is not an IPv4 mask", mask)
	}

	out := make(net.IP, net.IPv4len)
	for i := 0; i < net.IPv4len; i++ {
		out[i] = ip4[i] | ^mask[i]
	}
	return out, nil
}

// InterfaceSender is the PacketSender wakerd uses. For every up,
// non-loopback, non-point-to-point interface that has an IPv4 address, it
// binds a socket to that interface's own IP and sends the payload to that
// interface's directed broadcast address, on every port in WakePorts.
//
// Binding to the interface IP (rather than 0.0.0.0) is what pins the egress
// interface; the directed broadcast address is what makes the packet a
// broadcast on that specific LAN segment.
type InterfaceSender struct {
	// Logf receives one line naming the interfaces the packet went out on.
	// Defaults to log.Printf when nil.
	Logf func(format string, args ...any)
}

// DirectedSender is the package-level InterfaceSender used by wakerd.
var DirectedSender PacketSender = InterfaceSender{}

func (s InterfaceSender) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// SendBroadcast implements PacketSender.
func (s InterfaceSender) SendBroadcast(payload []byte) error {
	ifaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("list interfaces: %w", err)
	}

	var (
		used    []string
		lastErr error
	)

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 ||
			iface.Flags&net.FlagLoopback != 0 ||
			iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			lastErr = err
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}
			bcast, err := DirectedBroadcast(ipNet.IP, ipNet.Mask)
			if err != nil {
				lastErr = err
				continue
			}
			if err := sendFrom(ipNet.IP, bcast, payload); err != nil {
				lastErr = err
				continue
			}
			used = append(used, fmt.Sprintf("%s (%s -> %s)", iface.Name, ipNet.IP, bcast))
		}
	}

	if len(used) == 0 {
		// No usable interface: fall back to the limited broadcast so a
		// single-homed host still works rather than failing outright.
		if err := DefaultSender.SendBroadcast(payload); err != nil {
			if lastErr != nil {
				return fmt.Errorf("no usable broadcast interface (%v) and limited broadcast failed: %w", lastErr, err)
			}
			return fmt.Errorf("no usable broadcast interface and limited broadcast failed: %w", err)
		}
		s.logf("wol: no directed-broadcast interface found, fell back to %s", broadcastAddr)
		return nil
	}

	s.logf("wol: magic packet sent on ports %v via %s", WakePorts, strings.Join(used, ", "))
	return nil
}

// sendFrom sends payload to dst on every port in WakePorts, from a socket
// bound to src.
func sendFrom(src, dst net.IP, payload []byte) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: src.To4(), Port: 0})
	if err != nil {
		return fmt.Errorf("bind udp socket on %s: %w", src, err)
	}
	defer conn.Close()

	if err := setBroadcast(conn); err != nil {
		return fmt.Errorf("enable broadcast on %s: %w", src, err)
	}

	for _, port := range WakePorts {
		if _, err := conn.WriteToUDP(payload, &net.UDPAddr{IP: dst, Port: port}); err != nil {
			return fmt.Errorf("send to %s:%d: %w", dst, port, err)
		}
	}
	return nil
}
