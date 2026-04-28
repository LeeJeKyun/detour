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
	"syscall"

	"detour/internal/admin"
	"detour/internal/dnat"
	"detour/internal/redirect"
)

func parseEndpoint(s string) (redirect.Endpoint, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return redirect.Endpoint{}, fmt.Errorf("invalid IP:PORT %q: %w", s, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return redirect.Endpoint{}, fmt.Errorf("invalid IP address %q", host)
	}
	ip = ip.To4()
	if ip == nil {
		return redirect.Endpoint{}, fmt.Errorf("only IPv4 supported, got %q", host)
	}
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || p == 0 {
		return redirect.Endpoint{}, fmt.Errorf("invalid port %q", portStr)
	}
	return redirect.Endpoint{IP: ip, Port: uint16(p)}, nil
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
		fromStr  = flag.String("from", "", "intercepted destination IP:PORT (e.g. 1.2.3.4:5000)")
		toStr    = flag.String("to", "", "redirect target IP:PORT (e.g. 127.0.0.1:5001)")
		protoStr = flag.String("protocol", "both", "tcp|udp|both")
		verbose  = flag.Bool("v", false, "verbose logging")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: detour --from <IP:PORT> --to <IP:PORT> [--protocol tcp|udp|both]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

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
		log.Fatal("must run with elevated privileges (root on macOS/Linux, Administrator on Windows)")
	}

	rule := redirect.Rule{From: from, To: to, Proto: proto}
	r, err := redirect.New(rule, *verbose)
	if err != nil {
		log.Fatalf("init redirector: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Printf("signal received, cleaning up...")
	}()

	log.Printf("detour: %s — Ctrl+C to stop", rule)

	if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("exit error: %v", err)
		os.Exit(1)
	}
}
