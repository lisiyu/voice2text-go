//go:build windows

package main

// procattr_windows.go - 子进程创建属性：Windows 下隐藏控制台窗口。
import (
	"syscall"
)

func hiddenProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}
