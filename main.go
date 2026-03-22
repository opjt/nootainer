//go:build linux

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	switch os.Args[1] {
	case "run":
		run()
	case "child":
		child()
	case "container":
		container()
	case "pull":
		image := os.Args[2]
		tag := "latest"
		if len(os.Args) > 3 {
			tag = os.Args[3]
		}
		pull(image, tag)
	default:
		log.Fatal("unknown command")
	}
}

// run: systemd-run으로 위임받은 scope 안에서 child 실행
func run() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	args := append([]string{"--quiet", "--user", "--scope", "-p", "Delegate=yes", "--", exe, "child"}, os.Args[2:]...)
	cmd := exec.Command("systemd-run", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatal("run failed: ", err)
	}
}

// child: cgroup 설정 후 namespace 격리된 container 프로세스 실행
func child() {
	setCgroupV2()

	// overlayfs용 임시 디렉토리 생성
	containerDir, err := os.MkdirTemp("", "nootainer-*")
	must(err)

	cmd := exec.Command("/proc/self/exe", append([]string{"container"}, os.Args[2:]...)...)
	cmd.Env = append(os.Environ(), "NOOTAINER_DIR="+containerDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWUTS | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWNET,

		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
	os.RemoveAll(containerDir)
	if runErr != nil {
		log.Fatal(runErr)
	}
}

// container: namespace 안에서 사용자 명령 실행
func container() {
	must(syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, ""))

	syscall.Sethostname([]byte("nootainer"))

	// overlayfs 설정
	containerDir := os.Getenv("NOOTAINER_DIR")
	wd, _ := os.Getwd()
	rootfs := fmt.Sprintf("rootfs_%s", strings.ToLower(os.Args[2]))

	lowerdir := filepath.Join(wd, rootfs)
	upper := filepath.Join(containerDir, "upper")
	work := filepath.Join(containerDir, "work")
	merged := filepath.Join(containerDir, "merged")
	must(os.MkdirAll(upper, 0755))
	must(os.MkdirAll(work, 0755))
	must(os.MkdirAll(merged, 0755))

	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lowerdir, upper, work)
	must(syscall.Mount("overlay", merged, "overlay", 0, opts))

	pivotRoot(merged)
	setupSeccomp()
	dropCapabilities()

	cmd := exec.Command(os.Args[3], os.Args[4:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatal("exec failed: ", err)
	}
}

func pivotRoot(rootfs string) {
	// 1. rootfs를 bind mount (mount point로 만들기)
	must(syscall.Mount(rootfs, rootfs, "", syscall.MS_BIND|syscall.MS_REC, ""))

	// 2. 기존 루트를 넣을 디렉토리 생성
	putOld := filepath.Join(rootfs, "put_old")
	must(os.MkdirAll(putOld, 0700))

	// 3. pivot_root: 루트 교체
	must(syscall.PivotRoot(rootfs, putOld))

	// 4. 새 루트로 이동
	if err := os.Chdir("/"); err != nil {
		log.Fatal("chdir failed: ", err)
	}

	// 5. proc mount (put_old 해제 전에 해야 부모 proc superblock 참조 유지)
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		log.Fatal("mount proc failed: ", err)
	}

	// 6. 기존 루트 해제 및 정리
	if err := syscall.Unmount("/put_old", syscall.MNT_DETACH); err != nil {
		log.Fatal("unmount failed: ", err)
	}
	if err := os.Remove("/put_old"); err != nil {
		log.Fatal("remove failed: ", err)
	}
}

func getCgroupPath() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		log.Fatal(err)
	}
	line := strings.TrimSpace(string(data))
	parts := strings.SplitN(line, ":", 3)
	return filepath.Join("/sys/fs/cgroup", parts[2])
}
func setCgroupV2() {
	cgroupPath := getCgroupPath() // scope 경로
	must(os.WriteFile(filepath.Join(cgroupPath, "pids.max"), []byte("20"), 0644))
	must(os.WriteFile(filepath.Join(cgroupPath, "memory.max"), []byte("100M"), 0644))
}
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
