//go:build linux

package main

import (
	"log"
	"syscall"
)

const (
	PR_CAPBSET_DROP = 24
)

// 드롭할 capability 목록
var dropCaps = []uintptr{
	21, // CAP_SYS_ADMIN
	13, // CAP_NET_RAW
	19, // CAP_SYS_PTRACE
	16, // CAP_SYS_MODULE
	22, // CAP_SYS_BOOT
	23, // CAP_SYS_NICE
	24, // CAP_SYS_RESOURCE
	25, // CAP_SYS_TIME
}

func dropCapabilities() {
	for _, cap := range dropCaps {
		// prctl(PR_CAPBSET_DROP, cap)
		// RawSyscall6 사용
		_, _, err := syscall.RawSyscall(syscall.SYS_PRCTL, PR_CAPBSET_DROP, cap, 0)
		if err != 0 {
			log.Fatal(err)
		}
	}

}
