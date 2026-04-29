package cli

import (
	"net"
	"testing"

	"detour/internal/dnat"
)

func TestParseEndpoint_Valid(t *testing.T) {
	cases := []struct {
		in       string
		wantIP   string
		wantPort uint16
	}{
		{"1.2.3.4:5000", "1.2.3.4", 5000},
		{"127.0.0.1:65535", "127.0.0.1", 65535},
		{"0.0.0.0:1", "0.0.0.0", 1},
		{"255.255.255.255:80", "255.255.255.255", 80},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseEndpoint(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.IP.Equal(net.ParseIP(tc.wantIP).To4()) {
				t.Errorf("IP = %s, want %s", got.IP, tc.wantIP)
			}
			if got.Port != tc.wantPort {
				t.Errorf("Port = %d, want %d", got.Port, tc.wantPort)
			}
			if got.String() != tc.in {
				t.Errorf("String() = %q, want %q", got.String(), tc.in)
			}
		})
	}
}

func TestParseEndpoint_Invalid(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"no port separator", "1.2.3.4"},
		{"port zero", "1.2.3.4:0"},
		{"port out of range", "1.2.3.4:99999"},
		{"port not numeric", "1.2.3.4:abc"},
		{"hostname rejected", "example.com:5000"},
		{"localhost rejected", "localhost:5000"},
		{"IPv6 rejected", "[::1]:5000"},
		{"empty host", ":5000"},
		{"trailing colon", "1.2.3.4:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseEndpoint(tc.in); err == nil {
				t.Errorf("ParseEndpoint(%q) succeeded, want error", tc.in)
			}
		})
	}
}

func TestParseProto(t *testing.T) {
	cases := []struct {
		in   string
		want dnat.Protocol
	}{
		{"tcp", dnat.ProtoTCP},
		{"TCP", dnat.ProtoTCP},
		{"Tcp", dnat.ProtoTCP},
		{"udp", dnat.ProtoUDP},
		{"UDP", dnat.ProtoUDP},
		{"both", dnat.ProtoBoth},
		{"BOTH", dnat.ProtoBoth},
		{"", dnat.ProtoBoth},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseProto(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("ParseProto(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseProto_Invalid(t *testing.T) {
	for _, in := range []string{"sctp", "icmp", "  tcp  ", "tcp/udp", "random"} {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseProto(in); err == nil {
				t.Errorf("ParseProto(%q) succeeded, want error", in)
			}
		})
	}
}
