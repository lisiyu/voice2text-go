//go:build windows

package main

// platform_windows.go - Windows 平台小函数。
import (
	"fmt"
	"os"
	"os/exec"
)

// defaultWhisperPort whisper-server 默认端口（Windows：保持 8080，行为不变）。
const defaultWhisperPort = 8080

// platformExeName：Windows 下后端文件名原样（含 .exe 后缀）。
func platformExeName(name string) string {
	return name
}

// binaryName：安装完成提示里用的主程序名。
func binaryName() string {
	return "voice2text.exe"
}

// openConfig 用记事本打开配置文件本体。
func openConfig() {
	_ = exec.Command("notepad.exe", cfgPath()).Start()
}

// toggleCommandMain：`voice2text toggle` 子命令仅 Linux Wayland 模式支持，
// Windows 下打印提示并以 1 退出（保证共享 main() 在两个平台都能编译）。
func toggleCommandMain() int {
	fmt.Fprintf(os.Stderr, "toggle 子命令仅 Linux 版支持（Wayland 模式经 socket 触发）\n")
	return 1
}
