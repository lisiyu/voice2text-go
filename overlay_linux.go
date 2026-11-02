//go:build linux

package main

// overlay_linux.go - Linux 版不复刻 Win32 分层窗口 + GDI 波形。
// ShowOverlay 映射为两件事：
//  1) 托盘提示更新（所有状态）；
//  2) 终态（已粘贴/失败/未识别/未录到/已达上限）发一条桌面通知。
// 录音中/转写中这类过程态只更新托盘，不弹通知，避免刷屏。
import (
	"os/exec"
	"strings"
	"sync"
)

var (
	ovMu        sync.Mutex
	ovState     int32
	ovOverLimit int32
)

func initOverlay() {
	logf("悬浮窗：Linux 版不创建窗口，状态走托盘 + 通知")
}

func ShowOverlay(text string, color uint32, state int32) {
	ovMu.Lock()
	ovState = state
	ovMu.Unlock()
	updateTrayStatus("状态：" + text)
	switch state {
	case stOK, stWarn:
		notifyUser("voice2text", text)
	}
}

func HideOverlay() {
	// Linux 下无窗口可藏；托盘状态由下一条 status.set 覆盖。
}

func SetOverLimit(v bool) {
	ovMu.Lock()
	if v {
		ovOverLimit = 1
	} else {
		ovOverLimit = 0
	}
	ovMu.Unlock()
}

func StartRecordAnim() {
	// Linux 下无波形动画。
}

// notifyUser 发一条桌面通知；通知工具缺失时只记日志，不报错打断主流程。
// 首选 gdbus 直调 org.freedesktop.Notifications.Notify：
// Ubuntu 26.04 的 notify-send 自带 AppArmor 配置文件，在桥/服务上下文里
// 连不上通知服务（实测报“权限不够”），而 gdbus 直调同一服务实测可用。
// gdbus 不可用时再回退 notify-send。
func notifyUser(title, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if _, err := exec.LookPath("gdbus"); err == nil {
		args := []string{"call", "--session",
			"--dest", "org.freedesktop.Notifications",
			"--object-path", "/org/freedesktop/Notifications",
			"--method", "org.freedesktop.Notifications.Notify",
			"voice2text", "0", "", title, text, "[]", "{}", "3000"}
		if err := exec.Command("gdbus", args...).Run(); err == nil {
			return
		} else {
			logf("gdbus 通知失败，改试 notify-send: %v", err)
		}
	}
	cmd := exec.Command("notify-send", title, text)
	if err := cmd.Run(); err != nil {
		logf("notify-send 失败（已跳过通知）: %v", err)
	}
}
