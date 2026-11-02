//go:build linux

package main

// session_linux.go - Linux 桌面会话检测与会话环境补全。
// 桥进程不在用户桌面会话里（读不到 XDG_SESSION_TYPE、
// 连不上 user bus），因此从桥上拉起本程序时，
// 下面几个变量经常是缺失的；按当前 uid 的标准路径补齐，
// 这样托盘/通知/wtype/gsettings 才能连上用户会话。
import (
	"fmt"
	"os"
)

// linuxSessionType 返回 "wayland" / "x11" / ""（未知）。
// 优先 $XDG_SESSION_TYPE；桥上该变量通常缺失，
// 此时按 Wayland socket 文件是否存在判定。
func linuxSessionType() string {
	if t := os.Getenv("XDG_SESSION_TYPE"); t != "" {
		return t
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return "wayland"
	}
	uid := os.Getuid()
	if _, err := os.Stat(fmt.Sprintf("/run/user/%d/wayland-0", uid)); err == nil {
		return "wayland"
	}
	if os.Getenv("DISPLAY") != "" {
		return "x11"
	}
	return ""
}

// ensureSessionEnv 补齐缺失的会话环境变量（仅补缺失项，不覆盖已有值）。
func ensureSessionEnv() {
	uid := os.Getuid()
	runtimeDir := fmt.Sprintf("/run/user/%d", uid)
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		if _, err := os.Stat(runtimeDir); err == nil {
			os.Setenv("XDG_RUNTIME_DIR", runtimeDir)
		}
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		bus := "unix:path=" + runtimeDir + "/bus"
		if _, err := os.Stat(runtimeDir + "/bus"); err == nil {
			os.Setenv("DBUS_SESSION_BUS_ADDRESS", bus)
		}
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		if _, err := os.Stat(runtimeDir + "/wayland-0"); err == nil {
			os.Setenv("WAYLAND_DISPLAY", "wayland-0")
		}
	}
}
