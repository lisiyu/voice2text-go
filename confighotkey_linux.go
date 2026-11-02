//go:build linux

package main

// confighotkey_linux.go - 兜底配置热键。
//   X11：单独抓 Ctrl+Shift+Alt+C（与主热键循环不同的连接，互不干扰），
//     按下后用 xdg-open 打开配置文件。
//   Wayland：无全局热键兜底，托盘菜单“编辑配置”是唯一入口。
import (
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

func configHotkeyLoop() {
	if linuxSessionType() != "x11" {
		logf("配置热键：Wayland 下无全局热键兜底，请用托盘菜单“编辑配置”")
		return
	}
	conn, err := xgb.NewConn()
	if err != nil {
		logf("配置热键：连接 X 服务器失败: %v", err)
		return
	}
	defer conn.Close()
	root := xRootWindow(conn)
	kc, err := xKeysymToKeycode(conn, 0x43) // C
	if err != nil {
		logf("配置热键：C 键无对应 keycode: %v", err)
		return
	}
	mods := uint16(xproto.ModMaskControl | xproto.ModMaskShift | xproto.ModMask1)
	xGrabKeycodeWithLocks(conn, root, kc, mods)
	logf("配置热键：已抓 Ctrl+Shift+Alt+C")
	for {
		ev, err := conn.WaitForEvent()
		if err != nil {
			logf("配置热键：事件循环退出: %v", err)
			return
		}
		if kp, ok := ev.(xproto.KeyPressEvent); ok && kp.Detail == kc {
			logf("配置热键 Ctrl+Shift+Alt+C 触发，打开配置")
			openConfig()
			time.Sleep(500 * time.Millisecond) // 防重复触发
		}
	}
}
