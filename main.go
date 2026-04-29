//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"detour/internal/admin"
	"detour/internal/cli"
	"detour/internal/runtime"
)

// Build metadata, populated at link time via -ldflags "-X main.version=...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

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

	from, err := cli.ParseEndpoint(*fromStr)
	if err != nil {
		log.Fatalf("parse --from: %v", err)
	}
	to, err := cli.ParseEndpoint(*toStr)
	if err != nil {
		log.Fatalf("parse --to: %v", err)
	}
	proto, err := cli.ParseProto(*protoStr)
	if err != nil {
		log.Fatal(err)
	}

	if !admin.IsElevated() {
		log.Fatal("must run as Administrator (WinDivert driver requires elevation)")
	}

	rule := runtime.Rule{From: from, To: to, Proto: proto}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// First Ctrl+C: graceful cleanup. Restore the default signal handler so a
	// second press kills the process immediately if shutdown hangs. A 3s
	// timer is the final fallback for stuck Recv calls in WinDivert.
	go func() {
		<-ctx.Done()
		log.Printf("signal received, cleaning up... (press Ctrl+C again to force exit)")
		stop()
		time.AfterFunc(3*time.Second, func() {
			log.Printf("cleanup timed out; forcing exit")
			os.Exit(0)
		})
	}()

	log.Printf("detour: %s — Ctrl+C to stop", rule)

	err = runtime.Run(ctx, rule, runtime.Options{
		Verbose: *verbose,
		OnStop: func(s runtime.Stats) {
			log.Printf("forward=%d reverse=%d packets", s.Forward, s.Reverse)
		},
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("exit error: %v", err)
		os.Exit(1)
	}
}
