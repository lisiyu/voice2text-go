//go:build linux

package main

// mutex_linux.go - 单实例锁（/tmp 下的 lock 文件 + flock 非阻塞独占）。
import (
	"os"
	"path/filepath"
	"syscall"
)

var lockFile *os.File

func acquireMutex() bool {
	path := filepath.Join(os.TempDir(), "voice2text.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		logf("单实例锁文件打开失败 %s: %v", path, err)
		return false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	lockFile = f // 持有到进程退出
	return true
}
