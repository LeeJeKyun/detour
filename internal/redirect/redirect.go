package redirect

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"detour/internal/dnat"
)

type Endpoint struct {
	IP   net.IP
	Port uint16
}

func (e Endpoint) String() string {
	return net.JoinHostPort(e.IP.String(), strconv.Itoa(int(e.Port)))
}

type Rule struct {
	From  Endpoint
	To    Endpoint
	Proto dnat.Protocol
}

func (r Rule) String() string {
	return fmt.Sprintf("%s -> %s (%s)", r.From, r.To, r.Proto)
}

// Redirector applies the rule for the lifetime of Run and removes it before
// returning. Implementations are platform-specific.
type Redirector interface {
	Run(ctx context.Context) error
}
