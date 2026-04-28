# detour

Windows에서 특정 `IP:PORT`로 향하는 TCP/UDP 트래픽을 다른 `IP:PORT`로 투명하게 리다이렉트하는 CLI. [WinDivert](https://github.com/basil00/WinDivert)로 커널 레벨에서 패킷을 가로채 destination NAT을 수행한다.

## Requirements

- Windows 7+ (x64)
- Go 1.23+ (빌드 시)
- 관리자 권한 (실행 시 — WinDivert 드라이버 로드)

## Build

```powershell
go build -o detour.exe .
```

WinDivert v2.2.2 SDK를 받아 `WinDivert.dll`과 `WinDivert64.sys`를 `detour.exe`와 같은 폴더에 배치한다.

- 다운로드: https://github.com/basil00/WinDivert/releases/tag/v2.2.2
- `x64/WinDivert.dll`, `x64/WinDivert64.sys` 두 파일을 `detour.exe` 옆으로 복사

## Usage

관리자 권한 PowerShell:

```powershell
.\detour.exe --from 1.2.3.4:5000 --to 127.0.0.1:5001
```

| 옵션 | 설명 |
|---|---|
| `--from <IP:PORT>` | 인터셉트할 원래 목적지 (필수) |
| `--to <IP:PORT>` | 새 목적지 (필수) |
| `--protocol tcp\|udp\|both` | 기본 `both` |
| `-v` | 필터 표현식 및 드롭 로그 출력 |

`Ctrl+C`로 종료하면 두 핸들이 닫히고 트래픽이 정상 경로로 복귀한다.

## How it works

- **forward 핸들**: `outbound + ip.DstAddr==FROM_IP + ...DstPort==FROM_PORT` 패킷 수신 → 목적지를 `TO`로 재작성 → 체크섬 재계산 → 재주입
- **reverse 핸들**: `inbound + ip.SrcAddr==TO_IP + ...SrcPort==TO_PORT` 응답 수신 → 출발지를 `FROM`으로 되돌림 → 호출 측 앱은 원래 목적지에서 응답이 온 것처럼 인식
- 시스템 전체 프로세스에 적용 (PID 필터 없음). 한 인스턴스당 1개 규칙. 다중 규칙이 필요하면 인스턴스를 여러 개 띄운다.

## Limitations (v1)

- IPv4만 지원 (IPv6 미지원)
- 루프백(127.0.0.1) 대상은 Windows 특성상 케이스에 따라 동작이 달라질 수 있음
- TCP MSS clamping 미적용 (경로상 MTU 차이 시 단편화 가능)

## License

`detour` 자체는 단순한 wrapper이며, 런타임 의존성인 WinDivert는 LGPLv2 / GPLv2 듀얼 라이선스다. 배포 시 WinDivert 라이선스 동봉이 필요하다.
