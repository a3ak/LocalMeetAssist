//go:build windows

package sysinfo

import (
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = kernel32.NewProc("GetDiskFreeSpaceExW")

func diskFreeBytes(path string) (uint64, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var freeToCaller, total, totalFree uint64
	result, _, callErr := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pointer)),
		uintptr(unsafe.Pointer(&freeToCaller)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if result == 0 {
		return 0, callErr
	}
	return freeToCaller, nil
}
