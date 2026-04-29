// Package cli holds CLI-input parsing helpers shared between the command-line
// entry point and any future GUI front-end. Pure Go, build-tag free, so it
// runs on every host and is straightforward to unit-test.
package cli

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"detour/internal/dnat"
)

// Endpoint is a validated IPv4 + port pair.
type Endpoint struct {
	IP   net.IP
	Port uint16
}

func (e Endpoint) String() string {
	return net.JoinHostPort(e.IP.String(), strconv.Itoa(int(e.Port)))
}

// ParseEndpoint accepts the canonical "IP:PORT" form and rejects anything
// that isn't a literal IPv4 with a non-zero port. Hostnames / IPv6 are
// intentionally rejected — see README "Limitations" for rationale.
func ParseEndpoint(s string) (Endpoint, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return Endpoint{}, fmt.Errorf("invalid IP:PORT %q: %w", s, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return Endpoint{}, fmt.Errorf("invalid IP address %q", host)
	}
	ip = ip.To4()
	if ip == nil {
		return Endpoint{}, fmt.Errorf("only IPv4 supported, got %q", host)
	}
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || p == 0 {
		return Endpoint{}, fmt.Errorf("invalid port %q", portStr)
	}
	return Endpoint{IP: ip, Port: uint16(p)}, nil
}

// ParseProto maps the user-facing "tcp"/"udp"/"both" keyword (case-insensitive,
// empty defaults to "both") to a dnat.Protocol.
func ParseProto(s string) (dnat.Protocol, error) {
	switch strings.ToLower(s) {
	case "both", "":
		return dnat.ProtoBoth, nil
	case "tcp":
		return dnat.ProtoTCP, nil
	case "udp":
		return dnat.ProtoUDP, nil
	}
	return 0, fmt.Errorf("invalid protocol %q (use tcp|udp|both)", s)
}
