// Package rules manages the persisted list of redirect rules used by the
// multi-rule GUI. The on-disk format is a small JSON document at
// %APPDATA%/detour/rules.json (or the platform equivalent of UserConfigDir).
//
// Pure Go and build-tag free — the package builds and tests on every host so
// the data layer can be exercised on macOS/Linux even though the only
// runtime caller is the Windows GUI.
package rules

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"detour/internal/cli"
	"detour/internal/dnat"
)

// CurrentVersion is the on-disk format version. Bumped only when a future
// schema change requires migration; older files with compatible shape still
// load.
const CurrentVersion = 1

// ErrNotFound signals the requested rule ID does not exist in the store.
var ErrNotFound = errors.New("rule not found")

// Rule is the persisted unit. ID is opaque and short; From/To carry parsed
// IPv4+port pairs so callers can hand them straight to runtime.Run.
type Rule struct {
	ID    string
	From  cli.Endpoint
	To    cli.Endpoint
	Proto dnat.Protocol
}

// fileRule mirrors Rule with string-typed fields for JSON. Storing endpoints
// as "IP:PORT" strings keeps the file human-readable and trivially editable
// by hand.
type fileRule struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	Proto string `json:"proto"`
}

type fileFormat struct {
	Version int        `json:"version"`
	Rules   []fileRule `json:"rules"`
}

// DefaultPath returns "<UserConfigDir>/detour/rules.json". On Windows that
// resolves to %APPDATA%\detour\rules.json; on macOS to
// ~/Library/Application Support/detour/rules.json.
func DefaultPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, "detour", "rules.json"), nil
}

// NewID returns an 8-char lower-case hex identifier. Collisions are
// vanishingly unlikely at the scale of a few dozen rules per user, and Add
// re-checks for them anyway.
func NewID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand on a sane host should never fail; fall back to a
		// deterministic-but-unique-enough value rather than panic.
		return fmt.Sprintf("err%05x", os.Getpid()&0xfffff)
	}
	return hex.EncodeToString(b[:])
}

// Conflicts reports whether two rules would overlap at the WinDivert filter
// level — i.e. their forward filters could both match the same packet. Two
// rules conflict iff they share From IP+Port and their protocols overlap
// (either side being "both" overlaps with everything).
func Conflicts(a, b Rule) bool {
	if !a.From.IP.Equal(b.From.IP) || a.From.Port != b.From.Port {
		return false
	}
	return protoOverlap(a.Proto, b.Proto)
}

func protoOverlap(a, b dnat.Protocol) bool {
	if a == dnat.ProtoBoth || b == dnat.ProtoBoth {
		return true
	}
	return a == b
}

// Store wraps a slice of rules with file-backed persistence. Methods are
// safe for concurrent use; a single mutex guards both the slice and the
// file write.
type Store struct {
	mu    sync.Mutex
	path  string
	rules []Rule
}

// NewStore returns a Store backed by path. The file is not read here — call
// Load explicitly. Pair with DefaultPath() for production, or a temp path
// for tests.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Path reports the file location this store reads from / writes to.
func (s *Store) Path() string {
	return s.path
}

// Load reads the file. Missing file is not an error — the in-memory list
// just stays empty. Malformed JSON or invalid rule fields surface as errors
// so the caller can warn the user instead of silently dropping data.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.rules = nil
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	out := make([]Rule, 0, len(f.Rules))
	seen := make(map[string]struct{}, len(f.Rules))
	for i, fr := range f.Rules {
		from, err := cli.ParseEndpoint(fr.From)
		if err != nil {
			return fmt.Errorf("rule[%d].from: %w", i, err)
		}
		to, err := cli.ParseEndpoint(fr.To)
		if err != nil {
			return fmt.Errorf("rule[%d].to: %w", i, err)
		}
		proto, err := cli.ParseProto(fr.Proto)
		if err != nil {
			return fmt.Errorf("rule[%d].proto: %w", i, err)
		}
		id := strings.TrimSpace(fr.ID)
		if id == "" {
			id = NewID()
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("rule[%d]: duplicate id %q", i, id)
		}
		seen[id] = struct{}{}
		out = append(out, Rule{ID: id, From: from, To: to, Proto: proto})
	}
	s.rules = out
	return nil
}

// Save writes the current list to disk atomically (temp file + rename).
// Creates the parent directory if missing.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	out := fileFormat{Version: CurrentVersion, Rules: make([]fileRule, 0, len(s.rules))}
	for _, r := range s.rules {
		out.Rules = append(out.Rules, fileRule{
			ID:    r.ID,
			From:  r.From.String(),
			To:    r.To.String(),
			Proto: r.Proto.String(),
		})
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal rules: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, s.path, err)
	}
	return nil
}

// Snapshot returns a defensive copy of the current rule list. The caller
// can iterate freely without holding the store lock.
func (s *Store) Snapshot() []Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Rule, len(s.rules))
	copy(out, s.rules)
	return out
}

// Len reports the current rule count.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rules)
}

// Get returns a copy of the rule with the given ID, or ErrNotFound.
func (s *Store) Get(id string) (Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.ID == id {
			return r, nil
		}
	}
	return Rule{}, ErrNotFound
}

// Add appends r, generating an ID if r.ID is empty. Fails if the rule
// conflicts with any existing rule or collides with an existing ID. The
// returned Rule carries the assigned ID.
func (s *Store) Add(r Rule) (Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ID == "" {
		r.ID = NewID()
	}
	for _, existing := range s.rules {
		if existing.ID == r.ID {
			return Rule{}, fmt.Errorf("rule id %q already exists", r.ID)
		}
		if Conflicts(existing, r) {
			return Rule{}, fmt.Errorf("conflicts with existing rule %s (%s)", existing.ID, existing.From)
		}
	}
	s.rules = append(s.rules, r)
	if err := s.saveLocked(); err != nil {
		s.rules = s.rules[:len(s.rules)-1]
		return Rule{}, err
	}
	return r, nil
}

// Update replaces the rule sharing r.ID. Fails with ErrNotFound if no rule
// matches, or with a conflict error if the new spec collides with another
// rule (rules are compared against r excluding the rule being updated).
func (s *Store) Update(r Rule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, existing := range s.rules {
		if existing.ID == r.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	for i, existing := range s.rules {
		if i == idx {
			continue
		}
		if Conflicts(existing, r) {
			return fmt.Errorf("conflicts with existing rule %s (%s)", existing.ID, existing.From)
		}
	}
	prev := s.rules[idx]
	s.rules[idx] = r
	if err := s.saveLocked(); err != nil {
		s.rules[idx] = prev
		return err
	}
	return nil
}

// Remove deletes the rule with the given ID. Returns ErrNotFound if no such
// rule exists.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.rules {
		if existing.ID == id {
			removed := s.rules[i]
			s.rules = append(s.rules[:i], s.rules[i+1:]...)
			if err := s.saveLocked(); err != nil {
				// Re-insert at the original position to keep order stable.
				s.rules = append(s.rules[:i], append([]Rule{removed}, s.rules[i:]...)...)
				return err
			}
			return nil
		}
	}
	return ErrNotFound
}
