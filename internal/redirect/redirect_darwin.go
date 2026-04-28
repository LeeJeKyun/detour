//go:build darwin

package redirect

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"detour/internal/dnat"
)

const anchorParent = "detour"

func New(rule Rule, verbose bool) (Redirector, error) {
	return &darwinRedirector{
		rule:    rule,
		verbose: verbose,
		anchor:  fmt.Sprintf("%s/%d", anchorParent, os.Getpid()),
	}, nil
}

type darwinRedirector struct {
	rule    Rule
	verbose bool
	anchor  string
}

func (d *darwinRedirector) Run(ctx context.Context) error {
	if err := d.checkAnchorReference(); err != nil {
		return err
	}

	enabled, err := pfEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		if err := d.runPfctl(nil, "-e"); err != nil {
			return fmt.Errorf("enable pf: %w", err)
		}
		defer func() {
			if err := d.runPfctl(nil, "-d"); err != nil {
				log.Printf("warning: failed to disable pf on exit: %v", err)
			}
		}()
	}

	rules := d.buildRules()
	if d.verbose {
		log.Printf("anchor %s rules:\n%s", d.anchor, rules)
	}
	if err := d.runPfctl([]byte(rules), "-a", d.anchor, "-f", "-"); err != nil {
		return fmt.Errorf("load anchor %s: %w", d.anchor, err)
	}
	defer func() {
		if err := d.runPfctl(nil, "-a", d.anchor, "-F", "all"); err != nil {
			log.Printf("warning: failed to flush anchor %s: %v", d.anchor, err)
		}
	}()

	<-ctx.Done()
	return nil
}

func (d *darwinRedirector) buildRules() string {
	var b strings.Builder
	if d.rule.Proto == dnat.ProtoTCP || d.rule.Proto == dnat.ProtoBoth {
		fmt.Fprintf(&b, "rdr pass proto tcp from any to %s port %d -> %s port %d\n",
			d.rule.From.IP, d.rule.From.Port, d.rule.To.IP, d.rule.To.Port)
	}
	if d.rule.Proto == dnat.ProtoUDP || d.rule.Proto == dnat.ProtoBoth {
		fmt.Fprintf(&b, "rdr pass proto udp from any to %s port %d -> %s port %d\n",
			d.rule.From.IP, d.rule.From.Port, d.rule.To.IP, d.rule.To.Port)
	}
	return b.String()
}

func (d *darwinRedirector) runPfctl(stdin []byte, args ...string) error {
	cmd := exec.Command("pfctl", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if d.verbose {
		log.Printf("pfctl %s", strings.Join(args, " "))
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pfctl %s: %w (stderr: %s)",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func pfEnabled() (bool, error) {
	out, err := exec.Command("pfctl", "-s", "info").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("pfctl -s info: %w (output: %s)",
			err, strings.TrimSpace(string(out)))
	}
	return strings.Contains(string(out), "Status: Enabled"), nil
}

func (d *darwinRedirector) checkAnchorReference() error {
	var stderr bytes.Buffer
	cmd := exec.Command("pfctl", "-s", "nat")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("pfctl -s nat: %w (stderr: %s)",
			err, strings.TrimSpace(stderr.String()))
	}
	if hasUsableAnchorRef(string(out)) {
		return nil
	}

	// Show the user what is currently registered so it's obvious whether their
	// /etc/pf.conf edit took effect, then give a copy-pasteable fix.
	var current []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "rdr-anchor") {
			current = append(current, "  "+line)
		}
	}
	currentBlock := "  (none)"
	if len(current) > 0 {
		currentBlock = strings.Join(current, "\n")
	}

	return fmt.Errorf(`pf main ruleset has no rdr-anchor "%s/*" reference.

Currently registered rdr-anchor entries:
%s

To install it once (persists across reboots):
  echo 'rdr-anchor "%s/*"' | sudo tee -a /etc/pf.conf
  sudo pfctl -f /etc/pf.conf

Then re-run detour. See README for details.`, anchorParent, currentBlock, anchorParent)
}

// hasUsableAnchorRef returns true if the main NAT ruleset references a
// rdr-anchor that detour's sub-anchor will fall under: either the explicit
// "detour/*" / "detour" anchor or a wildcard "*" anchor.
func hasUsableAnchorRef(natRuleset string) bool {
	for _, line := range strings.Split(natRuleset, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "rdr-anchor") {
			continue
		}
		if strings.Contains(line, `"`+anchorParent+`/*"`) ||
			strings.Contains(line, `"`+anchorParent+`"`) ||
			strings.Contains(line, `"*"`) {
			return true
		}
	}
	return false
}
