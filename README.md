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
- Administrator privileges (run only — required to load the WinDivert driver)

## Build

```powershell
go build -o detour.exe .
```

For smaller release binaries:

```powershell
go build -ldflags "-s -w" -o detour.exe .
```

Cross-compile from macOS/Linux:

```sh
GOOS=windows go build -ldflags "-s -w" -o detour.exe .
```

## Usage

In an Administrator PowerShell:

```powershell
.\detour.exe --from 1.2.3.4:5000 --to 127.0.0.1:5001
```

| Flag | Description |
|---|---|
| `--from <IP:PORT>` | original destination to intercept (required) |
| `--to <IP:PORT>` | new destination (required) |
| `--protocol tcp\|udp\|both` | default `both` |
| `-v` | verbose logging — prints filter expressions and drop reasons |

Press `Ctrl+C` to stop. Both WinDivert handles close cleanly and traffic returns to its normal path.

## How it works

- **Forward handle**: receives outbound packets matching `ip.DstAddr == FROM_IP` + `DstPort == FROM_PORT`, rewrites the destination to `TO`, recalculates checksums, and reinjects the packet.
- **Reverse handle**: receives inbound packets matching `ip.SrcAddr == TO_IP` + `SrcPort == TO_PORT`, rewrites the source back to `FROM`, so the calling application sees responses as coming from the address it originally dialed.
- Applies system-wide (no PID filtering). One `--from`/`--to` rule per process — run multiple instances for multiple rules.

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
