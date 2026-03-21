//go:build linux

package main

import (
	"log"
	"syscall"
	"unsafe"
)

// man 2 seccomp
const (
	SECCOMP_MODE_FILTER = 2 // https://elixir.bootlin.com/linux/v6.19.9/source/include/uapi/linux/seccomp.h#L12
	SECCOMP_RET_ALLOW   = 0x7fff0000
	//ERRNO 말고도 여러 방법들이 있음
	SECCOMP_RET_ERRNO = 0x00050000 // https://elixir.bootlin.com/linux/v6.19.9/source/include/uapi/linux/seccomp.h#L42

	BPF_LD  = 0x00 //load
	BPF_JMP = 0x05
	BPF_RET = 0x06
	BPF_W   = 0x00 // Word 4byte
	BPF_ABS = 0x20
	BPF_JEQ = 0x10

	AUDIT_ARCH_AARCH64    = 0xC00000B7
	SECCOMP_DATA_NR_OFF   = 0 // syscall 번호 오프셋
	SECCOMP_DATA_ARCH_OFF = 4 // 아키텍처 오프셋

	// PR_SET_NO_NEW_PRIVS = 38
)

type sockFilter struct {
	Code uint16 // 연산코드
	Jt   uint8  //참이면
	Jf   uint8  //거짓
	K    uint32 //상수값
}

type sockFprog struct {
	Len    uint16
	Filter *sockFilter
}

// BPF 명령어 헬퍼
// stmt: Statement, 조건없이 그냥 실행하는 명령
func bpfStmt(code uint16, k uint32) sockFilter {
	return sockFilter{Code: code, K: k}
}

// jump: 조건부 실행(비교후 점프)
func bpfJump(code uint16, k uint32, jt, jf uint8) sockFilter {
	return sockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

// 차단할 syscall 목록
var blockedSyscalls = []uint32{
	142, // SYS_REBOOT (arm64)
	104, // SYS_KEXEC_LOAD (arm64)
	294, // SYS_KEXEC_FILE_LOAD (arm64)
}

func setupSeccomp() {
	filter := []sockFilter{
		// 아키텍처 체크
		bpfStmt(BPF_LD|BPF_W|BPF_ABS, SECCOMP_DATA_ARCH_OFF),
		bpfJump(BPF_JMP|BPF_JEQ, AUDIT_ARCH_AARCH64, 1, 0), // arm일 경우 1 점프해서 아래있는 err return을 무시한다
		bpfStmt(BPF_RET, SECCOMP_RET_ERRNO|uint32(syscall.EPERM)),

		// syscall 번호 로드
		bpfStmt(BPF_LD|BPF_W|BPF_ABS, SECCOMP_DATA_NR_OFF),

		// 차단 목록 체크 (매칭되면 ERRNO로, 아니면 다음으로)
		bpfJump(BPF_JMP|BPF_JEQ, 142, 4, 0), // SYS_REBOOT (arm64)
		bpfJump(BPF_JMP|BPF_JEQ, 104, 3, 0), // SYS_KEXEC_LOAD (arm64)
		bpfJump(BPF_JMP|BPF_JEQ, 294, 2, 0), // SYS_KEXEC_FILE_LOAD (arm64)
		bpfJump(BPF_JMP|BPF_JEQ, 97, 1, 0),  // unshare (arm64)

		// 허용
		bpfStmt(BPF_RET, SECCOMP_RET_ALLOW),
		// 차단
		bpfStmt(BPF_RET, SECCOMP_RET_ERRNO|uint32(syscall.EPERM)),
	}

	prog := sockFprog{
		Len:    uint16(len(filter)),
		Filter: &filter[0],
	}

	// no_new_privs 처리 (없으면 seccomp 로드 거부?)
	// https://www.kernel.org/doc/Documentation/prctl/no_new_privs.txt
	// _, _, errno := syscall.RawSyscall(
	// 	syscall.SYS_PRCTL,
	// 	PR_SET_NO_NEW_PRIVS, 1, // on
	// 	0,
	// )
	// if errno != 0 {
	// 	log.Fatal("set no_new_privs failed: ", errno)
	// }

	// seccomp 필터 로드
	_, _, errno := syscall.RawSyscall(
		syscall.SYS_PRCTL,
		syscall.PR_SET_SECCOMP,
		SECCOMP_MODE_FILTER,
		uintptr(unsafe.Pointer(&prog)),
	)
	if errno != 0 {
		log.Fatal("seccomp load failed: ", errno)
	}
}
