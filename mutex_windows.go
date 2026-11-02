//go:build windows

package main

// mutex_windows.go - 单实例锁（命名 Mutex）。
// 原 main.go 中的 acquireMutex，原样移至此。
import (
	"golang.org/x/sys/windows"
)

// mutex 句柄（持有到进程退出，防止重复启动）
var mutexHandle windows.Handle

func acquireMutex() bool {
	h, err := windows.CreateMutex(nil, true, windows.StringToUTF16Ptr("Global\\voice2text-single-instance"))
	// ERROR_ALREADY_EXISTS 必须先于通用 err 检查，否则 handle 泄漏
	if err == windows.ERROR_ALREADY_EXISTS {
		_ = windows.CloseHandle(h)
		return false
	}
	if err != nil {
		return false
	}
	mutexHandle = h
	return true
}
