# pivot_root 후 proc mount 실패 문제

nootainer에서 pivot_root 이후 `/proc` 마운트가 `operation not permitted`로 실패한 원인과 해결 과정을 정리한다.

## 1. 증상

```bash
$ ./nootainer run sh
2026/03/21 14:33:45 cwd: /Users/pjt/Projects/PJT/nootainer
2026/03/21 14:33:45 mount proc failed: operation not permitted
```

container() 함수에서 pivot_root 이후 `mount("proc", "/proc", "proc", 0, "")` 호출 시 실패 발생.

## 2. 혼란스러웠던 점

- user namespace 안에서 UID 0으로 매핑됨 (`id` > `uid=0(root)`)
- PID namespace도 정상 동작 (`echo $$` > PID n)
- pivot_root, bind mount 등 다른 mount 작업은 전부 성공
- AppArmor `restrict_unprivileged_userns`도 0

proc mount만 유독 실패하는 상황.

## 3. 원인 특정 과정

### unshare로 커널 지원 확인

```bash
# UID 매핑 없이 실패
$ unshare --user --pid --mount --fork sh -c "mount -t proc proc /proc"
mount: /proc: must be superuser to use mount.

# UID 매핑 있으면 성공
$ unshare --user --pid --mount --map-root-user --fork sh -c "mount -t proc proc /proc && echo OK"
OK
```

커널 자체는 user namespace 안에서의 proc mount를 지원한다. 문제는 nootainer 코드 흐름에 있었다.

### pivotRoot 단계별 디버깅

pivotRoot 함수 안의 각 단계 사이에 proc mount 테스트를 삽입했다:

```go
func tryProcMount(label string) {
    if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
        log.Printf("[%s] proc mount: %v", label, err)
    } else {
        log.Printf("[%s] proc mount: OK", label)
        syscall.Unmount("/proc", 0)
    }
}
```

결과:

```bash
[after pivot_root, before MS_PRIVATE] proc mount: OK
[after MS_PRIVATE]                    proc mount: OK
[after unmount put_old]               proc mount: operation not permitted
```

**`/put_old` unmount 이후부터 proc mount가 실패**한다.

## 4. 원인

### mount namespace와 부모 proc의 관계

`CLONE_NEWNS`로 새 mount namespace를 만들면 부모의 mount tree가 **그대로 복사**된다.
이 복사본에는 부모 PID namespace의 `/proc` 마운트도 포함되어 있다.

```bash
새 mount namespace (부모에서 복사됨)
├── /           # 부모의 루트
├── /proc       # 부모 PID namespace의 proc 마운트
├── /sys
└── ...
```

### pivot_root 후의 구조

```bash
/ (새 루트 = rootfs)
├── /proc       # 비어있음 (아직 마운트 안 됨)
└── /put_old    # 이전 루트
     ├── /proc  # 부모 PID namespace의 proc 마운트가 여기 있음
     └── ...
```

### 커널의 proc mount 검증

커널은 새 PID namespace에서 proc를 마운트할 때, **부모 PID namespace의 proc superblock이 현재 mount namespace에 존재하는지** 확인한다.

**superblock?**

- 파일시스템이 마운트될 때 커널이 담는 정보
  - 파일 시스템 타입(proc,ext4,tmpfs 등),
  - 마운트 옵션,
  - 해당 파일 시스템의 inode, 파일 목록 등
- proc의 경우 superblock이 PID namespace 마다 하나씩 만들어짐

이는 보안 체크다:

- 부모의 proc에 `hidepid=invisible` 같은 제한 옵션이 걸려있을 수 있음(다른 유저의 프로세스 정보 숨김옵션)
- 자식이 부모의 proc를 제거하고 제한 없는 새 proc를 마운트하면 보안 우회 가능
- 커널은 이를 방지하기 위해 부모의 proc 참조가 있는지 검증

만약 체크하지 않는다면 user,pid namespace를 생성하고 부모의 proc를 unmount후 제한없는 새 proc를 마운트 가능하다.

### 시간순 정리

| 시점                  | 부모 proc 위치         | proc mount |
| --------------------- | ---------------------- | ---------- |
| pivot_root 직후       | `/put_old/proc`에 존재 | OK         |
| MS_PRIVATE 적용 후    | `/put_old/proc`에 존재 | OK         |
| `/put_old` unmount 후 | 없음 (같이 제거됨)     | **EPERM**  |

`/put_old`을 unmount하면 그 아래의 모든 마운트(부모의 `/proc` 포함)가 함께 detach된다.  
부모의 proc 참조가 사라지면 커널이 보안 검증을 통과시킬 수 없어서 `EPERM`을 반환한다.

## 5. 해결

proc mount를 `/put_old` unmount **전에** 수행한다.

```go
func pivotRoot(rootfs string) {
    // 1. rootfs를 bind mount
    must(syscall.Mount(rootfs, rootfs, "", syscall.MS_BIND|syscall.MS_REC, ""))

    // 2. 기존 루트를 넣을 디렉토리 생성
    putOld := filepath.Join(rootfs, "put_old")
    must(os.MkdirAll(putOld, 0700))

    // 3. pivot_root
    must(syscall.PivotRoot(rootfs, putOld))

    // 4. 새 루트로 이동
    must(os.Chdir("/"))

    // 5. proc mount (put_old 해제 전에 해야 부모 proc superblock 참조 유지)
    must(syscall.Mount("proc", "/proc", "proc", 0, ""))

    // 6. 기존 루트 해제 및 정리 (이제 proc은 이미 마운트됨)
    must(syscall.Unmount("/put_old", syscall.MNT_DETACH))
    must(os.Remove("/put_old"))
}
```

한번 마운트된 proc는 이후에 부모 proc가 사라져도 정상 유지된다.

## 6. 핵심 교훈

- mount namespace는 mount 목록을 복사하는 것이지 propagation을 끊는 것이 아니다
- 커널은 proc mount 시 부모 PID namespace의 proc 참조를 요구한다 (보안)
- pivot_root 후의 작업 순서가 중요하다: proc mount > put_old unmount
