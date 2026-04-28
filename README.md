# detour

특정 `IP:PORT`로 향하는 TCP/UDP 트래픽을 다른 `IP:PORT`로 투명하게 리다이렉트하는 CLI. Windows / macOS를 지원하며 (Linux는 후속 지원), 각 OS의 네이티브 NAT 메커니즘에 위임한다.

| OS | 백엔드 | 권한 |
|---|---|---|
| Windows | [WinDivert](https://github.com/basil00/WinDivert) (드라이버 임베드) | Administrator |
| macOS | `pfctl` rdr anchor | root (`sudo`) |
| Linux | (stub — 후속 PR) | root |

Windows는 `WinDivert.dll`/`WinDivert64.sys`가 바이너리에 임베드되어 단일 `detour.exe`로 배포된다. macOS는 시스템 `pfctl`을 호출한다.

## Requirements

- Go 1.23+ (빌드 시)
- Windows 7+ (x64) 또는 macOS (PF 활성 가능 환경)

## Build

```sh
# 현재 OS용
go build -o detour .

# 크로스 빌드
GOOS=windows go build -o detour.exe .
GOOS=darwin  go build -o detour     .
```

## Usage (Windows)

관리자 권한 PowerShell:

```powershell
.\detour.exe --from 1.2.3.4:5000 --to 127.0.0.1:5001
```

## Usage (macOS)

먼저 한 번만, `/etc/pf.conf` 의 nat 영역에 detour anchor reference를 추가한다 (이미 같은 줄이 있으면 생략):

```
rdr-anchor "detour/*"
```

추가한 뒤 reload:

```sh
sudo pfctl -f /etc/pf.conf
```

이후 detour 실행:

```sh
sudo ./detour --from 1.2.3.4:5000 --to 127.0.0.1:5001
```

detour는 자기 PID 기반 sub-anchor (`detour/<pid>`) 에만 룰을 주입하고, 종료 시 해당 anchor를 비운다. PF가 비활성 상태였다면 자동으로 활성화하고 종료 시 다시 비활성화한다.

## 옵션

| 옵션 | 설명 |
|---|---|
| `--from <IP:PORT>` | 인터셉트할 원래 목적지 (필수) |
| `--to <IP:PORT>` | 새 목적지 (필수) |
| `--protocol tcp\|udp\|both` | 기본 `both` |
| `-v` | 디버그 로그 (Windows: 필터 표현식 / macOS: 실행한 pfctl 명령) |

`Ctrl+C` 로 종료하면 룰이 제거되고 트래픽이 정상 경로로 복귀한다.

## How it works

### Windows

- **forward 핸들**: `outbound + ip.DstAddr==FROM_IP + ...DstPort==FROM_PORT` 패킷 수신 → 목적지를 `TO`로 재작성 → 체크섬 재계산 → 재주입
- **reverse 핸들**: `inbound + ip.SrcAddr==TO_IP + ...SrcPort==TO_PORT` 응답 수신 → 출발지를 `FROM`으로 되돌림 → 호출 측 앱은 원래 목적지에서 응답이 온 것처럼 인식
- 시스템 전체 프로세스에 적용 (PID 필터 없음)

### macOS

- `pfctl -a detour/<pid> -f -` 로 sub-anchor에 `rdr pass proto {tcp,udp} from any to FROM_IP port FROM_PORT -> TO_IP port TO_PORT` 룰을 주입
- 커널 PF가 NAT 변환을 수행하므로 userspace에서 패킷을 만지지 않는다
- 종료 시 `pfctl -a detour/<pid> -F all` 로 anchor flush

### 공통

- 한 인스턴스당 1 규칙. 다중 규칙이 필요하면 인스턴스를 여러 개 띄운다.

## Runtime layout (Windows)

첫 실행 시 임베드된 WinDivert 파일이 다음 경로로 추출된다 (콘텐츠 해시 기반):

```
%PROGRAMDATA%\detour\runtime-<sha256-prefix>\
  ├── WinDivert.dll
  └── WinDivert64.sys
```

같은 버전을 재실행하면 캐시 디렉토리를 재사용하며, 다른 버전을 빌드해 배포하면 별도 디렉토리가 생성된다.

## Limitations (v1)

- IPv4만 지원 (IPv6 미지원)
- Windows: 루프백(127.0.0.1) 대상은 OS 특성상 케이스에 따라 동작이 달라질 수 있음
- Windows: TCP MSS clamping 미적용 (경로상 MTU 차이 시 단편화 가능)
- macOS: PF가 외부 도구(예: VPN, Apple System Extension) 에 의해 강하게 관리되는 환경에서는 anchor reference 추가가 reload로 덮어써질 수 있음
- 비정상 종료(SIGKILL 등) 시 macOS에서는 `detour/<pid>` anchor가 잔존할 수 있음. 수동 정리: `sudo pfctl -a detour/<pid> -F all`

## License

`detour`는 **GPLv3** 라이선스로 배포된다. 자세한 내용은 [LICENSE](LICENSE) 참조.

런타임 의존성인 [WinDivert](https://github.com/basil00/WinDivert)는 **LGPLv3 / GPLv2** 듀얼 라이선스이며, 본 프로젝트는 LGPLv3 조항을 따른다. WinDivert의 라이선스 전문은 빌드/배포 시 함께 동봉할 것 (현재 저장소 기준 `third_party/WinDivert-2.2.2-A/LICENSE`).
