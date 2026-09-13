package wol

import (
	"bytes"
	"testing"
)

func TestBuildMagicPacket_LengthAndRepetition(t *testing.T) {
	packet, err := BuildMagicPacket("AA:BB:CC:DD:EE:FF")
	if err != nil {
		t.Fatalf("BuildMagicPacket: %v", err)
	}
	if len(packet) != magicPacketLen {
		t.Fatalf("len(packet) = %d, want %d", len(packet), magicPacketLen)
	}

	// First 6 bytes are 0xFF.
	if !bytes.Equal(packet[:6], []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Fatalf("header = % x, want six 0xFF bytes", packet[:6])
	}

	// Remaining 96 bytes are the MAC repeated 16 times.
	want := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	for i := 0; i < 16; i++ {
		got := packet[6+i*6 : 6+i*6+6]
		if !bytes.Equal(got, want) {
			t.Fatalf("repetition %d = % x, want % x", i, got, want)
		}
	}
}

func TestBuildMagicPacket_CaseInsensitive(t *testing.T) {
	packet, err := BuildMagicPacket("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("BuildMagicPacket: %v", err)
	}
	want := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	if !bytes.Equal(packet[6:12], want) {
		t.Fatalf("packet[6:12] = % x, want % x", packet[6:12], want)
	}
}

func TestBuildMagicPacket_Malformed(t *testing.T) {
	cases := []string{
		"",
		"AA:BB:CC:DD:EE",          // too few groups
		"AA:BB:CC:DD:EE:FF:00",    // too many groups
		"AA:BB:CC:DD:EE:GG",       // invalid hex
		"AABBCCDDEEFF",            // no separators
		"AA:BB:CC:DD:EE:F",        // short group
		"AA:BB:CC:DD:EE:FFF",      // long group
	}
	for _, mac := range cases {
		if _, err := BuildMagicPacket(mac); err == nil {
			t.Errorf("BuildMagicPacket(%q): expected error, got nil", mac)
		}
	}
}

type fakeSender struct {
	sent [][]byte
	err  error
}

func (f *fakeSender) SendBroadcast(payload []byte) error {
	f.sent = append(f.sent, payload)
	return f.err
}

func TestSend_InvokesSenderWithMagicPacket(t *testing.T) {
	sender := &fakeSender{}
	if err := Send(sender, "AA:BB:CC:DD:EE:FF"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sender.sent has %d entries, want 1", len(sender.sent))
	}
	want, _ := BuildMagicPacket("AA:BB:CC:DD:EE:FF")
	if !bytes.Equal(sender.sent[0], want) {
		t.Fatalf("sent payload mismatch")
	}
}

func TestSend_MalformedMACNeverReachesSender(t *testing.T) {
	sender := &fakeSender{}
	if err := Send(sender, "not-a-mac"); err == nil {
		t.Fatal("expected error for malformed MAC")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sender should not have been called, got %d sends", len(sender.sent))
	}
}
