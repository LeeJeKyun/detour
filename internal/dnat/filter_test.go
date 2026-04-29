package dnat

import (
	"net"
	"testing"
)

func TestProtocolString(t *testing.T) {
	cases := []struct {
		proto Protocol
		want  string
	}{
		{ProtoBoth, "both"},
		{ProtoTCP, "tcp"},
		{ProtoUDP, "udp"},
		{Protocol(99), "both"}, // unknown falls back to "both"
	}
	for _, tc := range cases {
		if got := tc.proto.String(); got != tc.want {
			t.Errorf("Protocol(%d).String() = %q, want %q", tc.proto, got, tc.want)
		}
	}
}

func TestBuildForwardFilter(t *testing.T) {
	ip := net.IPv4(1, 2, 3, 4)
	cases := []struct {
		name  string
		proto Protocol
		want  string
	}{
		{
			name:  "tcp only",
			proto: ProtoTCP,
			want:  "outbound and ip and ip.DstAddr == 1.2.3.4 and tcp.DstPort == 5000",
		},
		{
			name:  "udp only",
			proto: ProtoUDP,
			want:  "outbound and ip and ip.DstAddr == 1.2.3.4 and udp.DstPort == 5000",
		},
		{
			name:  "both",
			proto: ProtoBoth,
			want:  "outbound and ip and ip.DstAddr == 1.2.3.4 and (tcp.DstPort == 5000 or udp.DstPort == 5000)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildForwardFilter(ip, 5000, tc.proto)
			if got != tc.want {
				t.Errorf("filter mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestBuildReverseFilter(t *testing.T) {
	ip := net.IPv4(127, 0, 0, 1)
	got := BuildReverseFilter(ip, 5001, ProtoBoth)
	want := "inbound and ip and ip.SrcAddr == 127.0.0.1 and (tcp.SrcPort == 5001 or udp.SrcPort == 5001)"
	if got != want {
		t.Errorf("filter mismatch\n got: %s\nwant: %s", got, want)
	}
}
