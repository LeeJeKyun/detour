package wdembed

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

//go:embed assets/WinDivert.dll
//go:embed assets/WinDivert64.sys
var assets embed.FS

const (
	dllName = "WinDivert.dll"
	sysName = "WinDivert64.sys"
)

// Setup extracts the embedded WinDivert.dll and WinDivert64.sys into a
// per-machine, content-addressed runtime directory and pre-loads the DLL
// by absolute path. Subsequent LoadDLL("WinDivert.dll") calls (made by
// divert-go) return the same module handle, and WinDivert's internal
// driver-installation logic resolves WinDivert64.sys next to it.
func Setup() error {
	dir, err := runtimeDir()
	if err != nil {
		return fmt.Errorf("compute runtime dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %q: %w", dir, err)
	}
	for _, name := range []string{dllName, sysName} {
		if err := writeAssetIfMissing(dir, name); err != nil {
			return err
		}
	}
	if _, err := windows.LoadDLL(filepath.Join(dir, dllName)); err != nil {
		return fmt.Errorf("preload %s: %w", dllName, err)
	}
	return nil
}

func runtimeDir() (string, error) {
	h := sha256.New()
	for _, name := range []string{dllName, sysName} {
		data, err := assets.ReadFile("assets/" + name)
		if err != nil {
			return "", err
		}
		h.Write(data)
	}
	short := hex.EncodeToString(h.Sum(nil))[:12]
	progData := os.Getenv("PROGRAMDATA")
	if progData == "" {
		progData = `C:\ProgramData`
	}
	return filepath.Join(progData, "detour", "runtime-"+short), nil
}

func writeAssetIfMissing(dir, name string) error {
	target := filepath.Join(dir, name)
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data, err := assets.ReadFile("assets/" + name)
	if err != nil {
		return err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install %s: %w", target, err)
	}
	return nil
}
