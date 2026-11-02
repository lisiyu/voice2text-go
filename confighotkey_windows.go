//go:build windows

package main

// confighotkey_windows.go - 兜底配置热键（托盘菜单失效时也能打开配置）。
// 原 main.go 中的 configHotkeyLoop，原样移至此（打开动作走 openConfig 平台函数）。
import (
	"time"
)

// configHotkeyLoop 全局配置热键：Ctrl+Shift+Alt+C 打开配置文件（托盘菜单不可用时的兜底）。
// 用较冷门的四键组合，避免与各种应用的单 Ctrl+Shift+C 冲突。
func configHotkeyLoop() {
	const vkC = 0x43 // C
	const vkCtrl = 0x11
	const vkShift = 0x10
	const vkAlt = 0x12
	for {
		ctrl := isDown(vkCtrl)
		shift := isDown(vkShift)
		alt := isDown(vkAlt)
		c := isDown(vkC)
		if ctrl && shift && alt && c {
			logf("配置热键 Ctrl+Shift+Alt+C 触发，打开配置")
			openConfig()
			time.Sleep(500 * time.Millisecond) // 防重复触发
		}
		time.Sleep(50 * time.Millisecond)
	}
}
