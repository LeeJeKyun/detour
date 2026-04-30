package rules

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"detour/internal/cli"
	"detour/internal/dnat"
)

func mustEP(t *testing.T, s string) cli.Endpoint {
	t.Helper()
	ep, err := cli.ParseEndpoint(s)
	if err != nil {
		t.Fatalf("ParseEndpoint(%q): %v", s, err)
	}
	return ep
}

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return NewStore(filepath.Join(dir, "rules.json"))
}

func TestNewID_FormatAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID()
		if len(id) != 8 {
			t.Fatalf("NewID len = %d, want 8 (got %q)", len(id), id)
		}
		if strings.ToLower(id) != id {
			t.Fatalf("NewID not lower-case: %q", id)
		}
		if seen[id] {
			t.Fatalf("NewID collision after %d iterations: %q", i, id)
		}
		seen[id] = true
	}
}

func TestConflicts(t *testing.T) {
	mk := func(from, to string, proto dnat.Protocol) Rule {
		return Rule{
			ID:    "x",
			From:  mustEP(t, from),
			To:    mustEP(t, to),
			Proto: proto,
		}
	}
	cases := []struct {
		name string
		a, b Rule
		want bool
	}{
		{
			name: "same from + same proto (tcp)",
			a:    mk("1.2.3.4:5000", "127.0.0.1:5001", dnat.ProtoTCP),
			b:    mk("1.2.3.4:5000", "127.0.0.1:6001", dnat.ProtoTCP),
			want: true,
		},
		{
			name: "same from + different proto (tcp vs udp)",
			a:    mk("1.2.3.4:5000", "127.0.0.1:5001", dnat.ProtoTCP),
			b:    mk("1.2.3.4:5000", "127.0.0.1:6001", dnat.ProtoUDP),
			want: false,
		},
		{
			name: "same from + one is both",
			a:    mk("1.2.3.4:5000", "127.0.0.1:5001", dnat.ProtoBoth),
			b:    mk("1.2.3.4:5000", "127.0.0.1:6001", dnat.ProtoTCP),
			want: true,
		},
		{
			name: "same from + both vs udp",
			a:    mk("1.2.3.4:5000", "127.0.0.1:5001", dnat.ProtoUDP),
			b:    mk("1.2.3.4:5000", "127.0.0.1:6001", dnat.ProtoBoth),
			want: true,
		},
		{
			name: "different IP",
			a:    mk("1.2.3.4:5000", "127.0.0.1:5001", dnat.ProtoBoth),
			b:    mk("5.6.7.8:5000", "127.0.0.1:6001", dnat.ProtoBoth),
			want: false,
		},
		{
			name: "different port",
			a:    mk("1.2.3.4:5000", "127.0.0.1:5001", dnat.ProtoBoth),
			b:    mk("1.2.3.4:5050", "127.0.0.1:6001", dnat.ProtoBoth),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Conflicts(tc.a, tc.b); got != tc.want {
				t.Fatalf("Conflicts = %v, want %v", got, tc.want)
			}
			// Symmetric.
			if got := Conflicts(tc.b, tc.a); got != tc.want {
				t.Fatalf("Conflicts (swapped) = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStore_LoadMissingFile(t *testing.T) {
	s := tempStore(t)
	if err := s.Load(); err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if got := s.Len(); got != 0 {
		t.Fatalf("Len = %d, want 0", got)
	}
}

func TestStore_AddSaveLoadRoundTrip(t *testing.T) {
	s := tempStore(t)
	r1, err := s.Add(Rule{
		From:  mustEP(t, "1.2.3.4:5000"),
		To:    mustEP(t, "127.0.0.1:5001"),
		Proto: dnat.ProtoBoth,
	})
	if err != nil {
		t.Fatalf("Add r1: %v", err)
	}
	if r1.ID == "" {
		t.Fatalf("Add did not assign an ID")
	}
	if _, err := s.Add(Rule{
		From:  mustEP(t, "10.0.0.1:80"),
		To:    mustEP(t, "127.0.0.1:8080"),
		Proto: dnat.ProtoTCP,
	}); err != nil {
		t.Fatalf("Add r2: %v", err)
	}

	// Reload from disk and confirm both rules survive.
	s2 := NewStore(s.Path())
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := s2.Snapshot()
	if len(got) != 2 {
		t.Fatalf("loaded %d rules, want 2", len(got))
	}
	if got[0].From.String() != "1.2.3.4:5000" || got[0].Proto != dnat.ProtoBoth {
		t.Fatalf("rule[0] mismatch after reload: %+v", got[0])
	}
	if !got[0].From.IP.Equal(net.IPv4(1, 2, 3, 4).To4()) {
		t.Fatalf("rule[0].From.IP not parsed as IPv4: %v", got[0].From.IP)
	}
	if got[1].From.String() != "10.0.0.1:80" || got[1].Proto != dnat.ProtoTCP {
		t.Fatalf("rule[1] mismatch after reload: %+v", got[1])
	}
}

func TestStore_AddRejectsConflict(t *testing.T) {
	s := tempStore(t)
	if _, err := s.Add(Rule{
		From:  mustEP(t, "1.2.3.4:5000"),
		To:    mustEP(t, "127.0.0.1:5001"),
		Proto: dnat.ProtoTCP,
	}); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	_, err := s.Add(Rule{
		From:  mustEP(t, "1.2.3.4:5000"),
		To:    mustEP(t, "127.0.0.1:9999"),
		Proto: dnat.ProtoBoth,
	})
	if err == nil {
		t.Fatalf("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("error missing 'conflict': %v", err)
	}
	if got := s.Len(); got != 1 {
		t.Fatalf("Len = %d after rejected Add, want 1", got)
	}
}

func TestStore_AddRejectsDuplicateID(t *testing.T) {
	s := tempStore(t)
	r1, err := s.Add(Rule{
		ID:    "fixed-id",
		From:  mustEP(t, "1.2.3.4:5000"),
		To:    mustEP(t, "127.0.0.1:5001"),
		Proto: dnat.ProtoTCP,
	})
	if err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if r1.ID != "fixed-id" {
		t.Fatalf("Add overrode caller-supplied ID: %q", r1.ID)
	}
	if _, err := s.Add(Rule{
		ID:    "fixed-id",
		From:  mustEP(t, "5.6.7.8:9000"),
		To:    mustEP(t, "127.0.0.1:9001"),
		Proto: dnat.ProtoTCP,
	}); err == nil {
		t.Fatalf("expected duplicate-id error, got nil")
	}
}

func TestStore_UpdateAndRemove(t *testing.T) {
	s := tempStore(t)
	r1, _ := s.Add(Rule{
		From:  mustEP(t, "1.2.3.4:5000"),
		To:    mustEP(t, "127.0.0.1:5001"),
		Proto: dnat.ProtoTCP,
	})
	r2, _ := s.Add(Rule{
		From:  mustEP(t, "10.0.0.1:80"),
		To:    mustEP(t, "127.0.0.1:8080"),
		Proto: dnat.ProtoUDP,
	})

	// Update r1's destination.
	updated := r1
	updated.To = mustEP(t, "127.0.0.1:9999")
	if err := s.Update(updated); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := s.Get(r1.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if got.To.String() != "127.0.0.1:9999" {
		t.Fatalf("Update did not persist new To: %s", got.To)
	}

	// Update with a spec that conflicts with r2 → must fail.
	clash := r1
	clash.From = r2.From
	clash.Proto = dnat.ProtoBoth
	if err := s.Update(clash); err == nil {
		t.Fatalf("expected conflict on Update, got nil")
	}
	// Original r1 must remain unchanged.
	got, _ = s.Get(r1.ID)
	if got.From.String() != "1.2.3.4:5000" {
		t.Fatalf("Update rolled back incorrectly: From=%s", got.From)
	}

	// Update with unknown ID.
	missing := Rule{ID: "nope", From: r1.From, To: r1.To, Proto: r1.Proto}
	if err := s.Update(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update unknown ID: err=%v, want ErrNotFound", err)
	}

	// Remove r2.
	if err := s.Remove(r2.ID); err != nil {
		t.Fatalf("Remove r2: %v", err)
	}
	if _, err := s.Get(r2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get removed: err=%v, want ErrNotFound", err)
	}

	// Remove again → ErrNotFound.
	if err := s.Remove(r2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Remove missing: err=%v, want ErrNotFound", err)
	}

	// Persistence after remove.
	s2 := NewStore(s.Path())
	if err := s2.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if s2.Len() != 1 {
		t.Fatalf("after Remove + reload: Len=%d, want 1", s2.Len())
	}
}

func TestStore_LoadMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if err := s.Load(); err == nil {
		t.Fatalf("expected parse error, got nil")
	}
}

func TestStore_LoadInvalidEndpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	body := `{"version":1,"rules":[{"id":"a","from":"not-an-ip","to":"127.0.0.1:5001","proto":"tcp"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	err := s.Load()
	if err == nil {
		t.Fatalf("expected error on invalid endpoint, got nil")
	}
	if !strings.Contains(err.Error(), "from") {
		t.Fatalf("error doesn't mention .from field: %v", err)
	}
}

func TestStore_LoadDuplicateID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	body := `{
        "version": 1,
        "rules": [
            {"id":"dup","from":"1.2.3.4:5000","to":"127.0.0.1:5001","proto":"tcp"},
            {"id":"dup","from":"5.6.7.8:5000","to":"127.0.0.1:5001","proto":"tcp"}
        ]
    }`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if err := s.Load(); err == nil {
		t.Fatalf("expected duplicate-id error, got nil")
	}
}

func TestStore_LoadAssignsIDIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	body := `{"version":1,"rules":[{"from":"1.2.3.4:5000","to":"127.0.0.1:5001","proto":"tcp"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := s.Snapshot()
	if len(got) != 1 {
		t.Fatalf("len=%d, want 1", len(got))
	}
	if got[0].ID == "" {
		t.Fatalf("ID was not auto-assigned")
	}
}

func TestStore_SaveCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	// Path under a nested directory that doesn't exist yet.
	path := filepath.Join(dir, "nested", "deep", "rules.json")
	s := NewStore(path)
	if _, err := s.Add(Rule{
		From:  mustEP(t, "1.2.3.4:5000"),
		To:    mustEP(t, "127.0.0.1:5001"),
		Proto: dnat.ProtoBoth,
	}); err != nil {
		t.Fatalf("Add (which triggers Save): %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestDefaultPath(t *testing.T) {
	// Just check it returns something non-empty containing "detour".
	p, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if !strings.Contains(p, "detour") {
		t.Fatalf("DefaultPath missing 'detour' segment: %q", p)
	}
	if !strings.HasSuffix(p, "rules.json") {
		t.Fatalf("DefaultPath missing rules.json suffix: %q", p)
	}
}
