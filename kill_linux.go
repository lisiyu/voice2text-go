//go:build linux

package main

// kill_linux.go - 清理残留 whisper-server。
// 优先用 PID 精确杀；无 PID 时退化为按名 pkill（仅当 pkill 可用时），
// 并记警告（可能影响本机其他同名进程，与 Windows 版 taskkill /IM 兜底同理）。
import (
	"os/exec"
	"syscall"
)

func killStaleWhisperServer() {
	wsMu.Lock()
	pid := 0
	if wsProc != nil && wsProc.Process != nil {
		pid = wsProc.Process.Pid
	}
	wsMu.Unlock()
	if pid != 0 {
		if err := syscall.Kill(pid, syscall.SIGKILL); err == nil {
			logf("已清理残留 whisper-server pid=%d", pid)
			wsMu.Lock()
			wsProc = nil
			wsMu.Unlock()
			return
		} else {
			logf("按 PID 清理残留 whisper-server pid=%d 失败: %v", pid, err)
		}
	}
	logf("警告: 按名 pkill whisper-server，可能影响本机其他实例/用户会话的同名进程")
	if _, err := exec.LookPath("pkill"); err == nil {
		if err := exec.Command("pkill", "-f", "whisper-server").Run(); err == nil {
			logf("已清理残留 whisper-server（按名）")
		}
	} else {
		logf("pkill 不可用，无法按名清理残留 whisper-server")
	}
}
