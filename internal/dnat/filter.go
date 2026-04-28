package dnat

import (
	"fmt"
	"net"
)

type Protocol int

const (
	ProtoBoth Protocol = iota
	ProtoTCP
	ProtoUDP
)

func (p Protocol) String() string {
	switch p {
	case ProtoTCP:
		return "tcp"
	case ProtoUDP:
		return "udp"
	default:
		return "both"
	}
}

func protoExpr(proto Protocol, side string, port uint16) string {
	switch proto {
	case ProtoTCP:
		return fmt.Sprintf("tcp.%s == %d", side, port)
	case ProtoUDP:
		return fmt.Sprintf("udp.%s == %d", side, port)
	default:
		return fmt.Sprintf("(tcp.%s == %d or udp.%s == %d)", side, port, side, port)
	}
}

func BuildForwardFilter(ip net.IP, port uint16, proto Protocol) string {
	return fmt.Sprintf("outbound and ip and ip.DstAddr == %s and %s",
		ip.String(), protoExpr(proto, "DstPort", port))
}

func BuildReverseFilter(ip net.IP, port uint16, proto Protocol) string {
	return fmt.Sprintf("inbound and ip and ip.SrcAddr == %s and %s",
		ip.String(), protoExpr(proto, "SrcPort", port))
}
