package dnat

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
)

// makeIPv4TCP builds a minimal IPv4+TCP packet with src/dst addresses and
// ports filled in. IHL=5 (20 bytes), no options. The TCP header is just the
// minimum 20 bytes — plenty for src/dst port rewrites at fixed offsets.
func makeIPv4TCP(srcIP, dstIP net.IP, srcPort, dstPort uint16) []byte {
	pkt := make([]byte, 40)
	pkt[0] = 0x45     // version 4, IHL 5
	pkt[9] = protoTCP // protocol = TCP
	copy(pkt[12:16], srcIP.To4())
	copy(pkt[16:20], dstIP.To4())
	binary.BigEndian.PutUint16(pkt[20:22], srcPort)
	binary.BigEndian.PutUint16(pkt[22:24], dstPort)
	return pkt
}

func makeIPv4UDP(srcIP, dstIP net.IP, srcPort, dstPort uint16) []byte {
	pkt := makeIPv4TCP(srcIP, dstIP, srcPort, dstPort)
	pkt[9] = protoUDP
	return pkt
}

func TestRewriteDest_TCP(t *testing.T) {
	pkt := makeIPv4TCP(
		net.IPv4(10, 0, 0, 1),
		net.IPv4(1, 2, 3, 4),
		40000, 5000,
	)
	if err := RewriteDest(pkt, net.IPv4(127, 0, 0, 1), 5001); err != nil {
		t.Fatalf("RewriteDest: %v", err)
	}
	gotIP := net.IPv4(pkt[16], pkt[17], pkt[18], pkt[19]).To4().String()
	if gotIP != "127.0.0.1" {
		t.Errorf("dst IP = %s, want 127.0.0.1", gotIP)
	}
	gotPort := binary.BigEndian.Uint16(pkt[22:24])
	if gotPort != 5001 {
		t.Errorf("dst port = %d, want 5001", gotPort)
	}
	// Source must be untouched.
	srcIP := net.IPv4(pkt[12], pkt[13], pkt[14], pkt[15]).To4().String()
	if srcIP != "10.0.0.1" {
		t.Errorf("src IP changed unexpectedly: %s", srcIP)
	}
	srcPort := binary.BigEndian.Uint16(pkt[20:22])
	if srcPort != 40000 {
		t.Errorf("src port changed unexpectedly: %d", srcPort)
	}
}

func TestRewriteSrc_UDP(t *testing.T) {
	pkt := makeIPv4UDP(
		net.IPv4(127, 0, 0, 1),
		net.IPv4(10, 0, 0, 1),
		5001, 40000,
	)
	if err := RewriteSrc(pkt, net.IPv4(1, 2, 3, 4), 5000); err != nil {
		t.Fatalf("RewriteSrc: %v", err)
	}
	gotIP := net.IPv4(pkt[12], pkt[13], pkt[14], pkt[15]).To4().String()
	if gotIP != "1.2.3.4" {
		t.Errorf("src IP = %s, want 1.2.3.4", gotIP)
	}
	gotPort := binary.BigEndian.Uint16(pkt[20:22])
	if gotPort != 5000 {
		t.Errorf("src port = %d, want 5000", gotPort)
	}
	// Destination must be untouched.
	dstIP := net.IPv4(pkt[16], pkt[17], pkt[18], pkt[19]).To4().String()
	if dstIP != "10.0.0.1" {
		t.Errorf("dst IP changed unexpectedly: %s", dstIP)
	}
}

func TestRewrite_DoesNotTouchChecksums(t *testing.T) {
	// Documented invariant: dnat.Rewrite* must not modify checksum bytes
	// (offsets 10..11 in the IP header, 16..17 in TCP, 6..7 in UDP).
	// The caller is responsible for recalculation.
	pkt := makeIPv4TCP(
		net.IPv4(10, 0, 0, 1),
		net.IPv4(1, 2, 3, 4),
		40000, 5000,
	)
	pkt[10], pkt[11] = 0xAB, 0xCD // IP checksum sentinel
	pkt[36], pkt[37] = 0xEF, 0x12 // TCP checksum sentinel (offset 16+20)
	if err := RewriteDest(pkt, net.IPv4(127, 0, 0, 1), 5001); err != nil {
		t.Fatalf("RewriteDest: %v", err)
	}
	if pkt[10] != 0xAB || pkt[11] != 0xCD {
		t.Errorf("IP checksum bytes were modified: %02X %02X", pkt[10], pkt[11])
	}
	if pkt[36] != 0xEF || pkt[37] != 0x12 {
		t.Errorf("TCP checksum bytes were modified: %02X %02X", pkt[36], pkt[37])
	}
}

func TestRewrite_Errors(t *testing.T) {
	cases := []struct {
		name string
		pkt  []byte
		want error
	}{
		{
			name: "empty",
			pkt:  []byte{},
			want: ErrShortPacket,
		},
		{
			name: "shorter than IPv4 header",
			pkt:  make([]byte, 19),
			want: ErrShortPacket,
		},
		{
			name: "not IPv4",
			pkt: func() []byte {
				p := make([]byte, 40)
				p[0] = 0x60 // version 6
				return p
			}(),
			want: ErrNotIPv4,
		},
		{
			name: "unsupported protocol (ICMP)",
			pkt: func() []byte {
				p := make([]byte, 40)
				p[0] = 0x45
				p[9] = 1 // ICMP
				return p
			}(),
			want: ErrUnsupported,
		},
		{
			name: "IHL claims 24 bytes but pkt only 22",
			pkt: func() []byte {
				p := make([]byte, 22)
				p[0] = 0x46 // IHL=6 → 24 bytes
				p[9] = protoTCP
				return p
			}(),
			want: ErrShortPacket,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RewriteDest(tc.pkt, net.IPv4(127, 0, 0, 1), 5001)
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRewrite_RejectsIPv6Address(t *testing.T) {
	pkt := makeIPv4TCP(
		net.IPv4(10, 0, 0, 1),
		net.IPv4(1, 2, 3, 4),
		40000, 5000,
	)
	v6 := net.ParseIP("::1")
	if err := RewriteDest(pkt, v6, 5001); !errors.Is(err, ErrNotIPv4) {
		t.Errorf("error = %v, want ErrNotIPv4", err)
	}
}

func TestRewrite_HonorsIHLOption(t *testing.T) {
	// Build a packet with IHL=6 (24-byte IP header, 4 bytes of options).
	pkt := make([]byte, 44)
	pkt[0] = 0x46 // version 4, IHL 6
	pkt[9] = protoTCP
	copy(pkt[12:16], net.IPv4(10, 0, 0, 1).To4())
	copy(pkt[16:20], net.IPv4(1, 2, 3, 4).To4())
	// TCP header starts at byte 24, ports at 24..28.
	binary.BigEndian.PutUint16(pkt[24:26], 40000)
	binary.BigEndian.PutUint16(pkt[26:28], 5000)

	if err := RewriteDest(pkt, net.IPv4(127, 0, 0, 1), 5001); err != nil {
		t.Fatalf("RewriteDest: %v", err)
	}
	if got := binary.BigEndian.Uint16(pkt[26:28]); got != 5001 {
		t.Errorf("dst port at IHL-relative offset = %d, want 5001 (IHL not honored)", got)
	}
}
