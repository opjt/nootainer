# nootainer

no-root + container. A rootless container runtime built from scratch in Go.

External dependencies: **none**. Standard library only.

- **Namespace isolation** — User, PID, UTS, Mount, IPC, Network
- **Cgroup v2** — PID/memory limits via rootless cgroup delegation
- **Seccomp BPF** — System call filtering with hand-written BPF bytecode
- **Capability drop** — Restrict dangerous capabilities (SYS_ADMIN, NET_RAW, SYS_PTRACE, etc.)
- **Image pull** — Pull images from Docker Hub via OCI Distribution Spec

## Architecture

```text
nootainer run <image> <cmd>
        │
        ▼
   ┌─────────┐   systemd-run --user --scope
   │   run    │   (cgroup delegation)
   └────┬────┘
        │      /proc/self/exe child
        ▼
   ┌─────────┐   cgroup v2 limits (pids.max, memory.max)
   │  child   │   clone(NEWUSER|NEWUTS|NEWNS|...)
   └────┬────┘
        │      /proc/self/exe container
        ▼
   ┌──────────┐  overlayfs mount → pivot_root
   │container │  seccomp BPF filter
   │          │  capability drop
   │          │  exec <cmd>
   └──────────┘
```

## Environment

- **Host OS**: macOS (Apple Silicon)
- **VM**: [Lima](https://github.com/lima-vm/lima) (Ubuntu, kernel 6.17, aarch64)
- **Language**: Go 1.25+

## Usage

### Pull an image

```bash
go build -o nootainer .
./nootainer pull alpine
./nootainer pull ubuntu
```

### Run a container

```bash
./nootainer run alpine /bin/sh
./nootainer run ubuntu /bin/bash
```
