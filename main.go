//go:build linux

package main

import (
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

	cmd := exec.Command("/proc/self/exe", append([]string{"container"}, os.Args[2:]...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWIPC | syscall.CLONE_NEWNET,
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
	if err := cmd.Run(); err != nil {
		log.Fatal(err)
	}
}

// container: namespace 안에서 사용자 명령 실행
func container() {
	syscall.Sethostname([]byte("nootainer"))
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		log.Fatal(err)
	}

	cmd := exec.Command(os.Args[2], os.Args[3:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatal(err)
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
