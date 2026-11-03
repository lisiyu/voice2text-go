//go:build linux

package main

// hook_linux.go - Linux 热键层入口。
//   X11：XGrabKey 抓键（纯 Go xgb），事件循环驱动 hookKeyDown；
//   Wayland：优先 evdev 按住模式（被动读 /dev/input/event*，见 evdev_linux.go），
//     读不到设备或无权限时回落 Unix socket 触发
//     （voice2text toggle），并把 hotkeyLoop 切到 toggle 语义
//     （按一下开始、再按一下停止，见 main.go 的 toggleMode 分支）。
//     注意：evdev 接管成功时不启动 socket——两路同时驱动 hookKeyDown
//     会互相打架（socket 的 150ms 脉冲会提前打断按住录音）；
//     此时 GNOME 快捷键绑定（Ctrl+Alt+V → voice2text toggle）
//     因无 socket 监听而失效，属预期行为，install.sh 仍保留该绑定
//     以备 evdev 失败时回落使用。
import (
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// hotkeyRebindCh：配置变更（watchConfig / 托盘切换 / 捕获）时，
// setHotkeyCode 往里发信号，X11 抓键循环收到后重新抓键。
var hotkeyRebindCh = make(chan uintptr, 1)

// setHotkeyCode 更新热键码；X11 下同时通知抓键循环重新抓键。
func setHotkeyCode(code uintptr) {
	hookVK.Store(code)
	select {
	case hotkeyRebindCh <- code:
	default: // 循环正忙时丢弃旧信号，下一次抓键读的是最新 cfg
	}
}

// startHotkeyLayer 是 main() 调用的平台热键层入口。
func startHotkeyLayer() {
	ensureSessionEnv()
	switch linuxSessionType() {
	case "wayland":
		if startEvdevHotkey() {
			logf("热键层：Wayland 模式，evdev 按住模式已启动（GNOME 快捷键 toggle 停用）")
		} else {
			toggleMode = true
			go socketListener()
			logf("热键层：Wayland 模式，evdev 不可用，走 socket 触发（voice2text toggle）")
		}
	case "x11":
		go xGrabLoop()
		logf("热键层：X11 模式，XGrabKey 抓键")
	default:
		// 无显示会话（例如桥上裸跑）：仍起 socket，至少 toggle 可用。
		toggleMode = true
		go socketListener()
		logf("热键层：未检测到显示会话，仅 socket 触发可用")
	}
}

// xGrabLoop：X11 抓键事件循环（独占一条 X 连接）。
func xGrabLoop() {
	conn, err := xgb.NewConn()
	if err != nil {
		logf("X11 热键：连接 X 服务器失败: %v", err)
		return
	}
	defer conn.Close()
	root := xRootWindow(conn)

	// 事件泵：WaitForEvent 是阻塞读，单独 goroutine 转发到 channel，
	// 主循环才能同时响应重抓键信号。
	evCh := make(chan xgb.Event, 32)
	go func() {
		for {
			ev, err := conn.WaitForEvent()
			if err != nil {
				close(evCh)
				return
			}
			if ev != nil {
				evCh <- ev
			}
		}
	}()

	var curKey xproto.Keycode
	var curMods uint16

	// grab 按当前配置抓键（含修饰键），返回是否成功。
	grab := func() bool {
		c := cfg()
		if c == nil {
			return false
		}
		ks := vkForName(c.Key)
		if ks == 0 {
			logf("X11 热键：无效按键 %q，已跳过抓键", c.Key)
			return false
		}
		kc, err := xKeysymToKeycode(conn, uint32(ks))
		if err != nil {
			logf("X11 热键：%v", err)
			return false
		}
		var mods uint16
		switch c.Mod {
		case "ctrl":
			mods = xproto.ModMaskControl
		case "alt":
			mods = xproto.ModMask1
		case "shift":
			mods = xproto.ModMaskShift
		}
		xGrabKeycodeWithLocks(conn, root, kc, mods)
		curKey, curMods = kc, mods
		logf("X11 热键：已抓键 %s mod=%s keycode=%d", c.Key, c.Mod, kc)
		return true
	}
	ungrab := func() {
		if curKey == 0 {
			return
		}
		xUngrabKeycodeWithLocks(conn, root, curKey, curMods)
		curKey = 0
	}

	grab()
	for {
		select {
		case <-hotkeyRebindCh:
			ungrab()
			grab()
		case ev, ok := <-evCh:
			if !ok {
				logf("X11 热键：事件通道关闭，抓键循环退出")
				return
			}
			switch e := ev.(type) {
			case xproto.KeyPressEvent:
				if e.Detail == curKey {
					hookKeyDown.Store(true)
				}
			case xproto.KeyReleaseEvent:
				if e.Detail == curKey {
					hookKeyDown.Store(false)
				}
			}
		}
	}
}
