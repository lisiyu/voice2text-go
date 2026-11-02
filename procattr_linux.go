//go:build linux

package main

// procattr_linux.go - 子进程创建属性：Linux 下无需隐藏窗口，返回 nil。
import (
	"syscall"
)

func hiddenProcAttr() *syscall.SysProcAttr {
	return nil
}
