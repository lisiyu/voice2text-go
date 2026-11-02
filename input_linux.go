//go:build linux

package main

// input_linux.go - 按键状态查询与修饰键映射。
// isDown 查 X11 QueryKeymap（Wayland 下无全局按键状态查询，
// 返回 false；toggle 模式不依赖它，见 hotkeyLoop）。
import (
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

var (
	isDownMu   sync.Mutex
	isDownConn *xgb.Conn
)

// isDown 判断某 keysym 对应的物理键当前是否按下（X11 QueryKeymap）。
func isDown(vk uintptr) bool {
	if linuxSessionType() == "wayland" {
		return false
	}
	isDownMu.Lock()
	defer isDownMu.Unlock()
	if isDownConn == nil {
		conn, err := xgb.NewConn()
		if err != nil {
			return false
		}
		isDownConn = conn
	}
	kc, err := xKeysymToKeycode(isDownConn, uint32(vk))
	if err != nil {
		return false
	}
	reply, err := xproto.QueryKeymap(isDownConn).Reply()
	if err != nil {
		return false
	}
	return reply.Keys[kc/8]&(1<<(kc%8)) != 0
}

// modVK：修饰键名 → keysym（与 keys_linux.go 的映射一致）。
func modVK(m string) uintptr {
	switch m {
	case "ctrl":
		return 0xFFE3
	case "alt":
		return 0xFFE9
	case "shift":
		return 0xFFE1
	}
	return 0
}
