//go:build windows

// Package runtime owns the WinDivert handle lifecycle and the userspace
// rewrite loop. It exposes Run(ctx, rule, opts) so both the CLI entry point
// and any future GUI front-end can drive the same kernel for a single rule.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	divert "github.com/imgk/divert-go"
	"golang.org/x/sync/errgroup"

	"detour/internal/cli"
	"detour/internal/dnat"
	"detour/internal/wdembed"
)

// forceCloseAfter caps how long we wait for WinDivertShutdown to wake a
// blocked Recv before yanking the handle closed. WinDivert v2 has a known
// quirk where Shutdown on an idle handle (empty queue, no incoming packets)
// fails to abort the pending overlapped I/O — Close, by contrast, reliably
// surfaces ERROR_INVALID_HANDLE which our runPath maps to a clean nil exit.
const forceCloseAfter = 1 * time.Second

// Rule is the redirection unit: outbound packets matching From are rewritten
// to To. Reverse-direction src is restored so the caller never sees the
// substitution.
type Rule struct {
	From  cli.Endpoint
	To    cli.Endpoint
	Proto dnat.Protocol
}

func (r Rule) String() string {
	return fmt.Sprintf("%s -> %s (%s)", r.From, r.To, r.Proto)
}

// Stats reports total packets relayed in each direction at shutdown.
type Stats struct {
	Forward uint64
	Reverse uint64
}

// Options controls runtime side-effects (logging, hooks).
type Options struct {
	Verbose bool
	// OnStop is invoked once after both handles drain, before Run returns.
	// Useful for surfacing packet counts in a CLI summary or GUI status bar.
	OnStop func(Stats)
	// ForwardCounter / ReverseCounter, when non-nil, receive every relayed
	// packet via atomic Add. Callers can poll them concurrently for live
	// status (e.g. a GUI that updates a label every second). The pointers
	// must remain valid for the duration of Run. If nil, runtime keeps the
	// counts internally and reports them only via OnStop.
	ForwardCounter *atomic.Uint64
	ReverseCounter *atomic.Uint64
}

// Run installs the rule, blocks until ctx is cancelled (or an unrecoverable
// error surfaces), and tears WinDivert down before returning. Run is safe to
// invoke from a goroutine; cancelling ctx is the only stop signal.
func Run(ctx context.Context, rule Rule, opts Options) error {
	if err := wdembed.Setup(); err != nil {
		return fmt.Errorf("install WinDivert runtime: %w", err)
	}

	fwdFilter := dnat.BuildForwardFilter(rule.From.IP, rule.From.Port, rule.Proto)
	revFilter := dnat.BuildReverseFilter(rule.To.IP, rule.To.Port, rule.Proto)
	if opts.Verbose {
		log.Printf("forward filter: %s", fwdFilter)
		log.Printf("reverse filter: %s", revFilter)
	}

	fwdH, err := divert.Open(fwdFilter, divert.LayerNetwork, 0, divert.FlagDefault)
	if err != nil {
		return fmt.Errorf("open forward handle: %w", err)
	}
	revH, err := divert.Open(revFilter, divert.LayerNetwork, 0, divert.FlagDefault)
	if err != nil {
		_ = fwdH.Close()
		return fmt.Errorf("open reverse handle: %w", err)
	}

	// closeOnce guards against double-Close: we may force-close from the
	// shutdown watcher when Shutdown fails to wake Recv, and the deferred
	// Close still runs at function exit.
	var fwdCloseOnce, revCloseOnce sync.Once
	closeFwd := func() { fwdCloseOnce.Do(func() { _ = fwdH.Close() }) }
	closeRev := func() { revCloseOnce.Do(func() { _ = revH.Close() }) }
	defer closeFwd()
	defer closeRev()

	// Use the caller-provided counters when supplied; otherwise allocate
	// local ones. Either way Run accumulates atomically so a polling reader
	// (GUI) sees a coherent value at any time.
	var localFwd, localRev atomic.Uint64
	fwdCount := opts.ForwardCounter
	if fwdCount == nil {
		fwdCount = &localFwd
	} else {
		fwdCount.Store(0)
	}
	revCount := opts.ReverseCounter
	if revCount == nil {
		revCount = &localRev
	} else {
		revCount.Store(0)
	}

	// Watch the parent ctx directly. Driving Shutdown from outside the
	// errgroup makes it idempotent regardless of which goroutine returns
	// first (graceful cancel vs. recv error).
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		if opts.Verbose {
			log.Printf("ctx cancelled; shutting down WinDivert handles")
		}
		_ = fwdH.Shutdown(divert.ShutdownBoth)
		_ = revH.Shutdown(divert.ShutdownBoth)
	}()

	g, _ := errgroup.WithContext(ctx)
	g.Go(func() error {
		return runPath(fwdH, func(buf []byte) error {
			return dnat.RewriteDest(buf, rule.To.IP, rule.To.Port)
		}, fwdCount, opts.Verbose, "forward")
	})
	g.Go(func() error {
		return runPath(revH, func(buf []byte) error {
			return dnat.RewriteSrc(buf, rule.From.IP, rule.From.Port)
		}, revCount, opts.Verbose, "reverse")
	})

	// g.Wait runs in a goroutine so we can race it against a force-close
	// timeout. If Shutdown actually wakes the Recv calls, this finishes
	// almost immediately and the timer is never observed.
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- g.Wait()
	}()

	var werr error
	select {
	case werr = <-waitDone:
		// Graceful drain.
	case <-mergedTimer(ctx, forceCloseAfter):
		if opts.Verbose {
			log.Printf("graceful drain timed out; force-closing WinDivert handles")
		}
		closeFwd()
		closeRev()
		werr = <-waitDone
	}
	<-shutdownDone

	if opts.OnStop != nil {
		opts.OnStop(Stats{Forward: fwdCount.Load(), Reverse: revCount.Load()})
	}

	if werr != nil && !isShutdownErr(werr) && !errors.Is(werr, context.Canceled) {
		return werr
	}
	return nil
}

// mergedTimer returns a channel that fires after d, but only if ctx is
// already done. Used to gate the force-close path: we only start the
// timer once the user has actually requested cancellation.
func mergedTimer(ctx context.Context, d time.Duration) <-chan time.Time {
	out := make(chan time.Time, 1)
	go func() {
		<-ctx.Done()
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case v := <-t.C:
			out <- v
		}
	}()
	return out
}

// isShutdownErr matches the WinDivert error codes that legitimately surface
// when WinDivertShutdown unblocks a pending Recv: ERROR_NO_DATA when the
// queue drains, ERROR_OPERATION_ABORTED when the I/O is cancelled, and
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
