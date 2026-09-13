// Package wol builds and broadcasts Wake-on-LAN "magic packets", used by
// POST /v1/wake to wake a machine on this runner's own local network (see
// ARCHITECTURE.md "Relay device" - a WoL broadcast cannot cross a router,
// so it only reaches machines on the same LAN segment as the runner that
// sends it).
package wol

import (
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// macLen is the number of bytes in a MAC address.
const macLen = 6

// magicPacketLen is 6 bytes of 0xFF followed by the target MAC repeated 16
// times: 6 + 6*16 = 102 bytes.
const magicPacketLen = 6 + macLen*16

// discardPort is the conventional UDP port Wake-on-LAN packets are sent to.
const discardPort = 9

// broadcastAddr is the limited broadcast address WoL packets are sent to.
const broadcastAddr = "255.255.255.255"

// ParseMAC parses a colon-separated hex MAC address (e.g.
// "AA:BB:CC:DD:EE:FF") into 6 raw bytes. It returns an error for malformed
// input: wrong number of groups, or non-hex groups.
func ParseMAC(mac string) ([macLen]byte, error) {
	var out [macLen]byte

	groups := strings.Split(mac, ":")
	if len(groups) != macLen {
		return out, fmt.Errorf("invalid MAC %q: expected %d colon-separated groups, got %d", mac, macLen, len(groups))
	}

	for i, g := range groups {
		if len(g) != 2 {
			return out, fmt.Errorf("invalid MAC %q: group %q is not 2 hex digits", mac, g)
		}
		b, err := hex.DecodeString(g)
		if err != nil {
			return out, fmt.Errorf("invalid MAC %q: group %q is not valid hex", mac, g)
		}
		out[i] = b[0]
	}

	return out, nil
}

// BuildMagicPacket builds the standard WoL magic packet for the given
// colon-separated MAC address: 6 bytes of 0xFF followed by the MAC repeated
// 16 times (102 bytes total). Returns an error if mac is malformed.
func BuildMagicPacket(mac string) ([]byte, error) {
	addr, err := ParseMAC(mac)
	if err != nil {
		return nil, err
	}

	packet := make([]byte, 0, magicPacketLen)
	for i := 0; i < 6; i++ {
		packet = append(packet, 0xFF)
	}
	for i := 0; i < 16; i++ {
		packet = append(packet, addr[:]...)
	}
	return packet, nil
}

// PacketSender broadcasts a raw payload on the local network. It's an
// interface so tests can fake the network send, mirroring how
// runner/internal/compose makes `docker compose` calls fakeable via its
// Runner interface.
type PacketSender interface {
	SendBroadcast(payload []byte) error
}

// udpSender is the real PacketSender, backed by a UDP broadcast socket.
type udpSender struct{}

// DefaultSender is the PacketSender used by Send.
var DefaultSender PacketSender = udpSender{}

func (udpSender) SendBroadcast(payload []byte) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("open udp socket: %w", err)
	}
	defer conn.Close()

	if err := setBroadcast(conn); err != nil {
		return fmt.Errorf("enable broadcast: %w", err)
	}

	dst := &net.UDPAddr{IP: net.ParseIP(broadcastAddr), Port: discardPort}
	if _, err := conn.WriteToUDP(payload, dst); err != nil {
		return fmt.Errorf("send broadcast: %w", err)
	}
	return nil
}

// Send builds the magic packet for mac and broadcasts it via sender.
func Send(sender PacketSender, mac string) error {
	packet, err := BuildMagicPacket(mac)
	if err != nil {
		return err
	}
	return sender.SendBroadcast(packet)
}
