//go:build linux

package main

// x11_linux.go - X11 连接辅助（纯 Go xgb，不引入 CGO）。
// 注意 xgb 连接的事件读取是单 goroutine 的：
// 抓键事件循环用自己的连接；短查询（QueryKeymap）用另一条
// 缓存连接，两边互不干扰。
import (
	"fmt"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

var (
	xQueryMu   sync.Mutex
	xQueryConn *xgb.Conn
)

// xQueryConnCached 返回一条缓存的 X 连接，仅供 QueryKeymap 这类
// 短同步查询使用（不要在这条连接上跑 WaitForEvent）。
func xQueryConnCached() (*xgb.Conn, error) {
	xQueryMu.Lock()
	defer xQueryMu.Unlock()
	if xQueryConn != nil {
		return xQueryConn, nil
	}
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	xQueryConn = conn
	return conn, nil
}

func xRootWindow(conn *xgb.Conn) xproto.Window {
	return xproto.Setup(conn).DefaultScreen(conn).Root
}

// xKeysymToKeycode 按当前键盘布局把 keysym 换算成 keycode。
// 实现：取整张键盘映射，找第一个包含该 keysym 的 keycode。
func xKeysymToKeycode(conn *xgb.Conn, keysym uint32) (xproto.Keycode, error) {
	setup := xproto.Setup(conn)
	count := byte(int(setup.MaxKeycode) - int(setup.MinKeycode) + 1)
	reply, err := xproto.GetKeyboardMapping(conn, setup.MinKeycode, count).Reply()
	if err != nil {
		return 0, err
	}
	per := int(reply.KeysymsPerKeycode)
	for kc := int(setup.MinKeycode); kc <= int(setup.MaxKeycode); kc++ {
		base := (kc - int(setup.MinKeycode)) * per
		for i := 0; i < per && base+i < len(reply.Keysyms); i++ {
			if uint32(reply.Keysyms[base+i]) == keysym {
				return xproto.Keycode(kc), nil
			}
		}
	}
	return 0, fmt.Errorf("keysym 0x%X 在当前键盘布局中无对应 keycode", keysym)
}

// xKeycodeToKeysym 取某 keycode 的第一个 keysym（捕获模式用）。
func xKeycodeToKeysym(conn *xgb.Conn, kc xproto.Keycode) (uint32, error) {
	reply, err := xproto.GetKeyboardMapping(conn, kc, 1).Reply()
	if err != nil {
		return 0, err
	}
	if len(reply.Keysyms) == 0 {
		return 0, fmt.Errorf("keycode %d 无 keysym", kc)
	}
	return uint32(reply.Keysyms[0]), nil
}

// xGrabKeycodeWithLocks 抓一个 keycode，附带 NumLock/CapsLock 的四种组合，
// 避免 NumLock/CapsLock 开着时热键失效（X11 常见坑）。
func xGrabKeycodeWithLocks(conn *xgb.Conn, root xproto.Window, kc xproto.Keycode, mods uint16) {
	for _, extra := range []uint16{0, xproto.ModMask2, xproto.ModMaskLock, xproto.ModMask2 | xproto.ModMaskLock} {
		cookie := xproto.GrabKeyChecked(conn, true, root, mods|extra, kc, xproto.GrabModeAsync, xproto.GrabModeAsync)
		if err := cookie.Check(); err != nil {
			logf("X11 抓键失败 keycode=%d mods=0x%X: %v（可能已被其他程序占用）", kc, mods|extra, err)
		}
	}
}

func xUngrabKeycodeWithLocks(conn *xgb.Conn, root xproto.Window, kc xproto.Keycode, mods uint16) {
	for _, extra := range []uint16{0, xproto.ModMask2, xproto.ModMaskLock, xproto.ModMask2 | xproto.ModMaskLock} {
		xproto.UngrabKey(conn, kc, root, mods|extra)
	}
}
