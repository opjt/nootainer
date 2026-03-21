# Rootless 컨테이너에서의 cgroup v2

nootainer를 구현하면서 겪은 cgroup v2 관련 이슈와 해결 과정을 정리한다.

## 1. cgroup v2 기본 구조

cgroup v2는 `/sys/fs/cgroup` 아래에 단일 트리(hierarchy)로 구성된다.
디렉토리를 만들면 그게 곧 cgroup이고, 커널이 컨트롤 파일들을 자동 생성한다.

```bash
/sys/fs/cgroup/
├── cgroup.controllers          # 사용 가능한 컨트롤러 목록 (읽기 전용)
├── cgroup.subtree_control      # 자식에게 활성화할 컨트롤러 (직접 써야 함)
├── cgroup.procs                # 이 cgroup에 속한 프로세스 PID
├── user.slice/
│   └── user-502.slice/
│       ├── session-4.scope/            # 로그인 세션
│       └── user@502.service/           # 유저 systemd 인스턴스
│           └── app.slice/
│               └── run-xxx.scope/      # systemd-run이 만든 scope
```

리소스 제한은 해당 cgroup 디렉토리의 파일에 값을 쓰면 적용된다:

```bash
echo "100M" > memory.max   # 100MB 메모리 제한
echo "20" > pids.max            # 최대 프로세스 20개
echo "50000 100000" > cpu.max   # 50% CPU 제한
```

## 2. cgroup.controllers vs cgroup.subtree_control

이 둘은 혼동하기 쉽지만 역할이 다르다.

| 파일                     | 설명                                                      | 기본값                             |
| ------------------------ | --------------------------------------------------------- | ---------------------------------- |
| `cgroup.controllers`     | 이 cgroup에서 **사용 가능한** 컨트롤러 (부모가 위임한 것) | 부모의 subtree_control에 의해 결정 |
| `cgroup.subtree_control` | **자식 cgroup에 활성화할** 컨트롤러                       | 비어있음 (직접 설정 필요)          |

`cgroup.controllers`에 `memory pids`가 보인다고 해서 자식 cgroup에 자동으로 `memory.max`, `pids.max`가 생기지 않는다.
반드시 `cgroup.subtree_control`에 `+memory +pids`를 써줘야 자식 cgroup에 해당 컨트롤러 파일이 생긴다.

```bash
# 부모 cgroup에서
echo "+memory +pids" > cgroup.subtree_control

# 이제 자식 cgroup을 만들면 memory.max, pids.max가 생긴다
mkdir child_cgroup
ls child_cgroup/memory.max  # 존재함
```

그리고 이 설정은 **한 단계씩만 적용**된다. 손자 cgroup에도 컨트롤러가 필요하면 자식의 `subtree_control`에도 따로 설정해야 한다.

## 3. No Internal Process 규칙

cgroup v2의 핵심 제약: **한 cgroup 디렉토리에 프로세스와 subtree_control 설정이 동시에 존재할 수 없다.**

```bash
# 이 상태에서는 subtree_control 수정 불가 (device or resource busy)
scope/
├── cgroup.procs              # 프로세스가 있음
├── cgroup.subtree_control    # 여기에 쓰기 불가
```

왜 이런 규칙이 있는가: 부모 cgroup에 프로세스가 있으면서 자식 cgroup에 리소스 제한이 걸려있으면,
부모의 프로세스는 제한을 안 받으므로 리소스 관리가 모호해진다. 커널이 이를 원천 차단하는 것이다.

### 해결: 임시 cgroup으로 프로세스 이동

```bash
# 1단계: 임시 cgroup 만들어서 프로세스를 빼냄
scope/
├── init/
│   └── cgroup.procs    # 프로세스를 여기로 이동
├── cgroup.subtree_control  # 이제 수정 가능

# 2단계: subtree_control 설정 후 실제 cgroup 생성
scope/
├── init/                   # 임시 (나중에 삭제)
├── nootainer/
│   ├── cgroup.subtree_control  # +memory +pids
│   └── container/
│       ├── memory.max          # 리소스 제한
│       ├── pids.max
│       └── cgroup.procs        # 컨테이너 프로세스
```

nootainer에서는 단순화를 위해 leaf cgroup을 따로 만들지 않고 scope 자체에 제한을 적용했다.
systemd-run이 만든 scope의 부모(`app.slice`)에 이미 `subtree_control`이 설정되어 있으므로,
scope의 `pids.max`와 `memory.max`에 직접 값을 쓰는 것으로 해결했다.
단, 이 방식은 나중에 scope 아래에 자식 cgroup을 만들려면 no internal process 규칙에 다시 부딪히므로 확장성은 포기한 구조다.

## 4. cgroup v2 hierarchy와 init user namespace

cgroup v2 hierarchy 전체는 **init user namespace가 소유**한다.

- 리눅스 부팅 시 커널이 cgroup 파일시스템 `sys/fs/cgroup`을 마운트하는데 이때 초기 namespace, 즉 루트가 속한 namespace가 이 계층의 소유자로 등록됨

`cgroup.procs`는 일반 파일이 아니라 커널의 cgroup 서브시스템이 관리하는 특수 파일이다.
여기에 쓸 때는 일반 파일 퍼미션 위에 **cgroup 전용 권한 체크**가 추가로 적용된다:

> writer의 user namespace가 cgroup 계층를 소유한 user namespace(init)와 같은가?

### CLONE_NEWUSER와의 충돌

`CLONE_NEWUSER`로 새 user namespace를 만들면, uid_map에 의해 호스트 uid 502가 컨테이너 uid 0으로 매핑된다.
파일시스템 레벨에서는 uid 502로 인식되어 `mkdir`, `WriteFile` 등은 정상 동작한다.

하지만 `cgroup.procs`에 쓸 때는 커널이 파일 퍼미션과 **별개로** user namespace를 체크한다.
새 user namespace에 있는 프로세스는 init user namespace의 cgroup hierarchy에 대한 쓰기 권한이 없다.

```bash
# child에서 cgroup.procs에 0(자기 자신)을 쓰려는 경우
child (새 user namespace, uid 0 = 호스트 uid 502)
  - cgroup.procs 파일 퍼미션: uid 502 소유, 쓰기 가능
  - cgroup 커널 체크: writer가 init user namespace에 있는가? X
  - 결과: permission denied
```

| 레벨              | 체크 대상                                      | 결과 |
| ----------------- | ---------------------------------------------- | ---- |
| 파일시스템        | uid 매핑 후 파일 소유자 일치 여부              | 통과 |
| cgroup 서브시스템 | writer의 user namespace == init user namespace | 실패 |

## 5. systemd delegation: 해결책

systemd는 init user namespace에 있기 때문에 cgroup 계층구조를 제어할 수 있고, `Delegate=yes`를 통해 특정 cgroup subtree의 제어 권한을 일반 유저에게 위임한다.

### systemd-run의 역할

```bash
systemd-run --user --scope -p Delegate=yes -- ./nootainer child sh
```

| 옵션              | 의미                                      |
| ----------------- | ----------------------------------------- |
| `--user`          | root systemd가 아닌 유저 systemd에 요청   |
| `--scope`         | 포그라운드에서 실행되는 일회성 scope 생성 |
| `-p Delegate=yes` | scope의 cgroup 소유권을 유저에게 위임     |

이 명령이 하는 일:

1. 유저 systemd에게 D-Bus로 "새 scope 만들어줘" 요청
2. systemd가 `app.slice/run-xxx.scope` 생성 (이미 subtree_control 설정됨)
3. 프로세스를 해당 scope 안에서 시작
4. scope의 cgroup 파일 소유권이 유저에게 위임됨

이렇게 하면 프로세스가 **이미 위임받은 scope 안에 있으므로** cgroup.procs에 쓸 수 있고,
공통 조상 문제도 발생하지 않는다.

### runc의 방식

runc(실제 컨테이너 런타임)는 `systemd-run` 명령을 호출하지 않고, D-Bus API의 `StartTransientUnit()` 메서드를 직접 호출한다.
`systemd-run`도 내부적으로 같은 D-Bus 메서드를 사용하므로 본질적으로 동일한 방식이다.

## 6. nootainer의 최종 구조

위 문제들을 해결한 nootainer의 프로세스 구조:

```bash
run()                        # 호스트, init user namespace
  │
  └─ systemd-run             # 위임받은 scope 생성
      │
      └─ child()             # scope 안, init user namespace
          │                  # - cgroup 리소스 제한 설정 (scope의 pids.max, memory.max)
          │
          └─ container()     # 새 user/pid/uts/mount/ipc/net namespace
                             # - hostname 설정, /proc 마운트
                             # - 사용자 명령 exec
```

3단계가 필요한 이유:

- **run → child**: systemd-run을 통해 위임받은 cgroup scope 확보
- **child → container**: cgroup 설정 (init user namespace에서) 후 namespace 격리된 프로세스 생성
- **container**: namespace 안에서 사용자 명령 실행

2단계로 줄일 수 없는 이유:

- `systemd-run`에 `CLONE_NEWUSER`를 걸면 systemd D-Bus 통신이 불가
- 새 user namespace 안에서는 cgroup.procs 쓰기 불가
- PID namespace는 `Unshare`해도 자식 프로세스만 적용되어 fork 필요
