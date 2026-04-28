//go:build windows

package redirect

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"

	divert "github.com/imgk/divert-go"
	"golang.org/x/sync/errgroup"

	"detour/internal/dnat"
	"detour/internal/wdembed"
)

func New(rule Rule, verbose bool) (Redirector, error) {
	if err := wdembed.Setup(); err != nil {
		return nil, fmt.Errorf("install WinDivert runtime: %w", err)
	}
	return &windowsRedirector{rule: rule, verbose: verbose}, nil
}

type windowsRedirector struct {
	rule    Rule
	verbose bool
}

func (w *windowsRedirector) Run(ctx context.Context) error {
	fwdFilter := dnat.BuildForwardFilter(w.rule.From.IP, w.rule.From.Port, w.rule.Proto)
	revFilter := dnat.BuildReverseFilter(w.rule.To.IP, w.rule.To.Port, w.rule.Proto)
	if w.verbose {
		log.Printf("forward filter: %s", fwdFilter)
		log.Printf("reverse filter: %s", revFilter)
	}

	fwdH, err := divert.Open(fwdFilter, divert.LayerNetwork, 0, divert.FlagDefault)
	if err != nil {
		return fmt.Errorf("open forward handle: %w", err)
	}
	defer fwdH.Close()

	revH, err := divert.Open(revFilter, divert.LayerNetwork, 0, divert.FlagDefault)
	if err != nil {
		return fmt.Errorf("open reverse handle: %w", err)
	}
	defer revH.Close()

	// Watch the parent ctx directly (not gctx). gctx would also fire when one
	// of the recv goroutines returns an error, but in that case we still want
	// shutdown to be triggered exactly once and not race with errgroup teardown.
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		if w.verbose {
			log.Printf("ctx cancelled; shutting down WinDivert handles")
		}
		_ = fwdH.Shutdown(divert.ShutdownBoth)
		_ = revH.Shutdown(divert.ShutdownBoth)
	}()

	var fwdCount, revCount atomic.Uint64
	g, _ := errgroup.WithContext(ctx)
	g.Go(func() error {
		return runPath(fwdH, func(buf []byte) error {
			return dnat.RewriteDest(buf, w.rule.To.IP, w.rule.To.Port)
		}, &fwdCount, w.verbose, "forward")
	})
	g.Go(func() error {
		return runPath(revH, func(buf []byte) error {
			return dnat.RewriteSrc(buf, w.rule.From.IP, w.rule.From.Port)
		}, &revCount, w.verbose, "reverse")
	})

	werr := g.Wait()
	// Make sure the shutdown trigger has actually run before returning, so
	// callers don't see a half-cleaned-up state.
	<-shutdownDone

	log.Printf("forward=%d reverse=%d packets", fwdCount.Load(), revCount.Load())
	if werr != nil && !isShutdownErr(werr) && !errors.Is(werr, context.Canceled) {
		return werr
	}
	return nil
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
