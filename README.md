# detour

[![CI](https://github.com/LeeJeKyun/detour/actions/workflows/ci.yml/badge.svg)](https://github.com/LeeJeKyun/detour/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/LeeJeKyun/detour)](https://github.com/LeeJeKyun/detour/releases)
[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![Go Reference](https://pkg.go.dev/badge/github.com/LeeJeKyun/detour.svg)](https://pkg.go.dev/github.com/LeeJeKyun/detour)

A Windows CLI that transparently redirects TCP/UDP traffic destined for one `IP:PORT` to another. Uses [WinDivert](https://github.com/basil00/WinDivert) to intercept packets at the kernel level and perform destination NAT in userspace.

`WinDivert.dll` and `WinDivert64.sys` are embedded in the binary, so **`detour.exe` ships as a single self-contained file** — no installer, no separate driver setup.

## Requirements

- Windows 7+ (x64)
- Go 1.23+ (build only)
- Administrator privileges (run only — required to load the WinDivert driver). The binary embeds a UAC manifest, so launching it from a non-elevated shell or by double-clicking automatically triggers the elevation prompt — no need to spawn an Administrator PowerShell yourself.

## Build

The binaries embed a UAC manifest via [`go-winres`](https://github.com/tc-hib/go-winres). One-time setup:

```sh
go install github.com/tc-hib/go-winres@latest
```

Then build both the CLI and the GUI:

```powershell
go-winres make --arch amd64
Copy-Item rsrc_windows_amd64.syso cmd\detour-gui\
go build -ldflags "-s -w" -o detour.exe .
go build -ldflags "-s -w -H=windowsgui" -o detour-gui.exe .\cmd\detour-gui
```

Cross-compile from macOS/Linux:

```sh
go-winres make --arch amd64
cp rsrc_windows_amd64.syso cmd/detour-gui/
GOOS=windows go build -ldflags "-s -w" -o detour.exe .
GOOS=windows go build -ldflags "-s -w -H=windowsgui" -o detour-gui.exe ./cmd/detour-gui
```

`go-winres make` reads `winres/winres.json` and produces `rsrc_windows_amd64.syso`, which `go build` automatically links into the executable. The `.syso` is build output (gitignored) — regenerate it whenever `winres/winres.json` changes. Each `main` package directory (`.` and `cmd/detour-gui/`) needs its own copy so the manifest is linked into both binaries.

Released zip archives include both binaries; you can grab them from the [Releases page](../../releases) instead of building from source.

## Usage

### CLI — `detour.exe`

```powershell
.\detour.exe --from 1.2.3.4:5000 --to 127.0.0.1:5001
```

A UAC prompt appears the first time the binary tries to load the WinDivert driver — accept it once per launch. After the prompt, the rule is active until you press `Ctrl+C`.

| Flag | Description |
|---|---|
| `--from <IP:PORT>` | original destination to intercept (required) |
| `--to <IP:PORT>` | new destination (required) |
| `--protocol tcp\|udp\|both` | default `both` |
| `-v` | verbose logging — prints filter expressions and drop reasons |
| `--version` | print version and exit |

Press `Ctrl+C` to stop. Both WinDivert handles close cleanly and traffic returns to its normal path.

### GUI — `detour-gui.exe`

A small native window for users who prefer not to keep an Administrator console open. Double-click to launch — the same UAC prompt appears, then a window listing your saved rules in a table.

Behavior:
- **Multi-rule** — every row is an independent rule with its own packet counters. Toggle the leftmost checkbox to start/stop a single rule; multiple rules can run at the same time.
- **Add / Edit / Delete** buttons (or double-click a row to edit) open a small modal dialog with `From` / `To` / `Protocol` fields. Inputs are validated live; **OK** stays disabled until both endpoints parse as valid IPv4:Port. A rule must be stopped before it can be edited.
- **Auto-saved** to `%APPDATA%\detour\rules.json`. Every Add/Edit/Delete writes the file atomically; the next launch restores the same list.
- **Live packet counts** (`Forward` / `Reverse`) refresh every second per row, plus a footer aggregate (`Active: N   Forward: M   Reverse: M`).
- **Tray icon** in the system notification area — gray when idle, green when at least one rule is running. Hover for the aggregate tooltip; left-click to reopen the window; right-click for **Open / Quit**.
- **Closing the window** with the X button (or Alt+F4) hides to the tray as long as any rule is running; only **Quit** (or X with everything stopped) terminates the process. The first hide triggers a balloon notification so you know the app is still alive.
- **Conflict detection** — two rules with the same `From IP:Port` and overlapping protocols (e.g. one `tcp`, another `both`) are rejected at Add/Edit time so the WinDivert filters never fight.

The CLI and GUI share the same packet-handling core (`internal/runtime`), so per-rule behavior with respect to traffic is identical — choose whichever interface is more convenient.

## How it works

- **Forward handle**: receives outbound packets matching `ip.DstAddr == FROM_IP` + `DstPort == FROM_PORT`, rewrites the destination to `TO`, recalculates checksums, and reinjects the packet.
- **Reverse handle**: receives inbound packets matching `ip.SrcAddr == TO_IP` + `SrcPort == TO_PORT`, rewrites the source back to `FROM`, so the calling application sees responses as coming from the address it originally dialed.
- Applies system-wide (no PID filtering). The CLI runs one rule per process; the GUI multiplexes multiple rules within a single process, each with its own pair of WinDivert handles.

## Runtime layout

On first run, the embedded WinDivert files are extracted to a content-hashed runtime directory:

```
%PROGRAMDATA%\detour\runtime-<sha256-prefix>\
  ├── WinDivert.dll
  └── WinDivert64.sys
```

Subsequent runs of the same binary reuse this cache. A different build (different file hashes) gets its own directory.

## Limitations (v1)

- IPv4 only (IPv6 not supported)
- Loopback (`127.0.0.1`) targets may behave inconsistently — Windows networking treats local-to-local traffic specially.
- No TCP MSS clamping — fragmentation may occur if the redirected path has a smaller MTU.

## License

`detour` is released under the **GPLv3** license. See [LICENSE](LICENSE) for details.

The runtime dependency [WinDivert](https://github.com/basil00/WinDivert) is dual-licensed **LGPLv3 / GPLv2**; this project relies on the LGPLv3 terms. When distributing builds, include the WinDivert license text alongside (the upstream copy lives at `third_party/WinDivert-2.2.2-A/LICENSE`).
