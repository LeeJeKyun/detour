//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	divert "github.com/imgk/divert-go"
	"golang.org/x/sync/errgroup"

	"detour/internal/admin"
	"detour/internal/dnat"
	"detour/internal/wdembed"
)

// Build metadata, populated at link time via -ldflags "-X main.version=...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type endpoint struct {
	IP   net.IP
	Port uint16
}

func (e endpoint) String() string {
	return net.JoinHostPort(e.IP.String(), strconv.Itoa(int(e.Port)))
}

func parseEndpoint(s string) (endpoint, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return endpoint{}, fmt.Errorf("invalid IP:PORT %q: %w", s, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return endpoint{}, fmt.Errorf("invalid IP address %q", host)
	}
	ip = ip.To4()
	if ip == nil {
		return endpoint{}, fmt.Errorf("only IPv4 supported, got %q", host)
	}
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || p == 0 {
		return endpoint{}, fmt.Errorf("invalid port %q", portStr)
	}
	return endpoint{IP: ip, Port: uint16(p)}, nil
}

func parseProto(s string) (dnat.Protocol, error) {
	switch strings.ToLower(s) {
	case "both", "":
		return dnat.ProtoBoth, nil
	case "tcp":
		return dnat.ProtoTCP, nil
	case "udp":
		return dnat.ProtoUDP, nil
	}
	return 0, fmt.Errorf("invalid --protocol %q (use tcp|udp|both)", s)
}

func main() {
	log.SetFlags(log.Ltime)

	var (
		fromStr     = flag.String("from", "", "intercepted destination IP:PORT (e.g. 1.2.3.4:5000)")
		toStr       = flag.String("to", "", "redirect target IP:PORT (e.g. 127.0.0.1:5001)")
		protoStr    = flag.String("protocol", "both", "tcp|udp|both")
		verbose     = flag.Bool("v", false, "verbose logging")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: detour --from <IP:PORT> --to <IP:PORT> [--protocol tcp|udp|both]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Printf("detour %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	if *fromStr == "" || *toStr == "" {
		flag.Usage()
		os.Exit(2)
	}
	from, err := parseEndpoint(*fromStr)
	if err != nil {
		log.Fatalf("parse --from: %v", err)
	}
	to, err := parseEndpoint(*toStr)
	if err != nil {
		log.Fatalf("parse --to: %v", err)
	}
	proto, err := parseProto(*protoStr)
	if err != nil {
		log.Fatal(err)
	}

	if !admin.IsElevated() {
		log.Fatal("must run as Administrator (WinDivert driver requires elevation)")
	}

	if err := wdembed.Setup(); err != nil {
		log.Fatalf("install WinDivert runtime: %v", err)
	}

	fwdFilter := dnat.BuildForwardFilter(from.IP, from.Port, proto)
	revFilter := dnat.BuildReverseFilter(to.IP, to.Port, proto)
	if *verbose {
		log.Printf("forward filter: %s", fwdFilter)
		log.Printf("reverse filter: %s", revFilter)
	}

	fwdH, err := divert.Open(fwdFilter, divert.LayerNetwork, 0, divert.FlagDefault)
	if err != nil {
		log.Fatalf("open forward handle: %v", err)
	}
	defer fwdH.Close()

	revH, err := divert.Open(revFilter, divert.LayerNetwork, 0, divert.FlagDefault)
	if err != nil {
		log.Fatalf("open reverse handle: %v", err)
	}
	defer revH.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var fwdCount, revCount atomic.Uint64

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		log.Printf("signal received, cleaning up... (press Ctrl+C again to force exit)")
		// Restore default Ctrl+C behavior so a second press kills the process
		// immediately if WinDivertShutdown can't unblock pending Recv calls.
		stop()
		_ = fwdH.Shutdown(divert.ShutdownBoth)
		_ = revH.Shutdown(divert.ShutdownBoth)
		// Hard fallback: certain WinDivert versions / driver states don't
		// reliably wake a blocked Recv on Shutdown. If g.Wait() hasn't
		// returned within 3s, force exit so the user is never stuck.
		time.AfterFunc(3*time.Second, func() {
			log.Printf("cleanup timed out (forward=%d reverse=%d); forcing exit",
				fwdCount.Load(), revCount.Load())
			os.Exit(0)
		})
	}()

	g, _ := errgroup.WithContext(ctx)

	g.Go(func() error {
		return runPath(fwdH, func(buf []byte) error {
			return dnat.RewriteDest(buf, to.IP, to.Port)
		}, &fwdCount, *verbose, "forward")
	})
	g.Go(func() error {
		return runPath(revH, func(buf []byte) error {
			return dnat.RewriteSrc(buf, from.IP, from.Port)
		}, &revCount, *verbose, "reverse")
	})

	log.Printf("detour: %s -> %s (%s) — Ctrl+C to stop", from, to, proto)

	werr := g.Wait()
	<-shutdownDone

	log.Printf("forward=%d reverse=%d packets", fwdCount.Load(), revCount.Load())
	if werr != nil && !isShutdownErr(werr) && !errors.Is(werr, context.Canceled) {
		log.Printf("exit error: %v", werr)
		os.Exit(1)
	}
}

// isShutdownErr matches the various WinDivert error codes that legitimately
// surface when WinDivertShutdown unblocks a pending Recv: ERROR_NO_DATA when
// the queue drains, ERROR_OPERATION_ABORTED when the I/O is cancelled, and
// ERROR_INVALID_HANDLE if the handle is closed concurrently.
func isShutdownErr(err error) bool {
	return errors.Is(err, divert.ErrNoData) ||
		errors.Is(err, divert.ErrOperationAborted) ||
		errors.Is(err, divert.ErrInvalidHandle)
}

func runPath(h *divert.Handle, rw func([]byte) error, count *atomic.Uint64, verbose bool, name string) error {
	buf := make([]byte, 65535)
	var addr divert.Address
	for {
		n, err := h.Recv(buf, &addr)
		if err != nil {
			if isShutdownErr(err) {
				if verbose {
					log.Printf("%s: recv stopped (%v)", name, err)
				}
				return nil
			}
			return fmt.Errorf("%s recv: %w", name, err)
		}
		pkt := buf[:n]
		if err := rw(pkt); err != nil {
			if verbose {
				log.Printf("%s: drop (rewrite failed: %v)", name, err)
			}
			continue
		}
		divert.CalcChecksums(pkt, &addr, 0)
		if _, err := h.Send(pkt, &addr); err != nil {
			if isShutdownErr(err) {
				return nil
			}
			return fmt.Errorf("%s send: %w", name, err)
		}
		count.Add(1)
	}
}
