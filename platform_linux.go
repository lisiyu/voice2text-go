//go:build linux

package main

// platform_linux.go - Linux 平台小函数。
import (
	"os/exec"
	"strings"
)

// defaultWhisperPort whisper-server 默认端口（Linux：18080）。
// 不用 8080：Linux 下 llama-server（llama.cpp server）默认常驻 8080，
// 且它的 /health 也回 200，易与 whisper-server 混淆（见 whisperServerHealthy）。
const defaultWhisperPort = 18080

// platformExeName：Linux 可执行文件没有 .exe 后缀，
// 清单里的 Windows 风格名字（如 whisper-server-vulkan.exe）
// 在此去掉后缀；无后缀的名字原样返回。
func platformExeName(name string) string {
	return strings.TrimSuffix(name, ".exe")
}

// binaryName：安装完成提示里用的主程序名。
func binaryName() string {
	return "voice2text"
}

// openConfig 用 xdg-open 打开配置文件本体（走默认编辑器）。
func openConfig() {
	_ = exec.Command("xdg-open", cfgPath()).Start()
}

// toggleCommandMain 是 `voice2text toggle` 子命令的入口（见 socket_linux.go）。
// Windows 版在 platform_windows.go 里给一个“不支持”的同名实现，
// 保证共享 main() 在两个平台都能编译。
