//go:build windows

package main

// kill_windows.go - 清理残留 whisper-server（taskkill）。
// 原 main.go 中的 killStaleWhisperServer，原样移至此
// （SysProcAttr 改走 hiddenProcAttr 平台函数）。
import (
	"fmt"
	"os/exec"
)

// killStaleWhisperServer 清理残留的 whisper-server.exe。
// 场景：父进程 voice2text 被 taskkill 强杀时，defer 不执行，whisper-server 成孤儿继续占端口，
// 下次启动若盲目复用会拿到异常结果（空格/乱码/截断）。
// 优先用 PID 精确杀，无 PID 时退化为按镜像名杀。
func killStaleWhisperServer() {
	wsMu.Lock()
	pid := 0
	if wsProc != nil && wsProc.Process != nil {
		pid = wsProc.Process.Pid
	}
	wsMu.Unlock()
	if pid != 0 {
		// 精确杀：只杀本实例启动的进程
		cmd := exec.Command("taskkill", "/F", "/PID", fmt.Sprintf("%d", pid))
		cmd.SysProcAttr = hiddenProcAttr()
		if err := cmd.Run(); err == nil {
			logf("已清理残留 whisper-server pid=%d", pid)
			wsMu.Lock()
			wsProc = nil
			wsMu.Unlock()
			return
		}
	}
	// 退化：PID 不可用（孤儿来自被强杀的旧实例），按镜像名清理。
	// 警告：这会杀掉本机所有 whisper-server.exe（含其他用户会话/其他实例），
	// 仅在 PID 路径不可用时作为最后兜底。
	logf("警告: 按镜像名 taskkill whisper-server.exe，可能影响本机其他实例/用户会话的同名进程")
	cmd := exec.Command("taskkill", "/F", "/IM", "whisper-server.exe")
	cmd.SysProcAttr = hiddenProcAttr()
	if err := cmd.Run(); err == nil {
		logf("已清理残留 whisper-server（按镜像名）")
	}
}
