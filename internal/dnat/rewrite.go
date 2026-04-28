package dnat

import (
	"encoding/binary"
	"errors"
	"net"
)

var (
	ErrShortPacket = errors.New("packet too short")
	ErrNotIPv4     = errors.New("not an IPv4 packet")
	ErrUnsupported = errors.New("unsupported transport protocol")
)

const (
	protoTCP byte = 6
	protoUDP byte = 17
)

func RewriteDest(pkt []byte, newIP net.IP, newPort uint16) error {
	return rewrite(pkt, newIP, newPort, true)
}

func RewriteSrc(pkt []byte, newIP net.IP, newPort uint16) error {
	return rewrite(pkt, newIP, newPort, false)
}

func rewrite(pkt []byte, newIP net.IP, newPort uint16, dst bool) error {
	if len(pkt) < 20 {
		return ErrShortPacket
	}
	if pkt[0]>>4 != 4 {
		return ErrNotIPv4
	}
	ihl := int(pkt[0]&0x0f) * 4
	if len(pkt) < ihl+4 {
		return ErrShortPacket
	}
	proto := pkt[9]
	if proto != protoTCP && proto != protoUDP {
		return ErrUnsupported
	}
	ip4 := newIP.To4()
	if ip4 == nil {
		return ErrNotIPv4
	}

	if dst {
		copy(pkt[16:20], ip4)
		binary.BigEndian.PutUint16(pkt[ihl+2:ihl+4], newPort)
	} else {
		copy(pkt[12:16], ip4)
		binary.BigEndian.PutUint16(pkt[ihl:ihl+2], newPort)
	}
	return nil
}
