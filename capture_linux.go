//go:build linux

package main

// capture_linux.go - 自定义快捷键捕获（托盘菜单 → 3 秒内按键生效）。
//   X11：XGrabKeyboard 抓键盘 3 秒，读 KeyPress 事件；
//     跳过修饰键、Esc 取消的逻辑与 Windows 版一致。
//   Wayland：普通应用拿不到全局按键，无法捕获，
//     提示用户直接编辑配置文件改 key。
import (
	"fmt"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

func captureKey() {
	if linuxSessionType() != "x11" {
		updateTrayStatus("状态：Wayland 下无法捕获按键，请用“编辑配置”直接改 key")
		logf("捕获：Wayland 下无全局按键捕获，改走配置文件")
		return
	}
	conn, err := xgb.NewConn()
	if err != nil {
		updateTrayStatus("状态：捕获失败（连不上 X）")
		logf("捕获：连接 X 服务器失败: %v", err)
		return
	}
	defer conn.Close()
	root := xRootWindow(conn)
	reply, err := xproto.GrabKeyboard(conn, true, root, xproto.TimeCurrentTime, xproto.GrabModeAsync, xproto.GrabModeAsync).Reply()
	if err != nil || reply.Status != 0 {
		updateTrayStatus("状态：捕获失败（键盘已被占用）")
		logf("捕获：GrabKeyboard 失败 status=%d err=%v", reply.Status, err)
		return
	}
	defer xproto.UngrabKeyboard(conn, xproto.TimeCurrentTime)

	capturing.Store(true)
	capturedVK.Store(0)
	capturedKeyDown.Store(false)
	updateTrayStatus("状态：请按下新热键（3秒内，Esc取消）")
	logf("进入自定义快捷键捕获（X11 抓键盘）")

	// 事件泵：抓键盘期间的按键事件写入共享原子变量，
	// 下面的轮询逻辑与 Windows 版 captureKey 完全一致。
	go func() {
		for {
			ev, err := conn.WaitForEvent()
			if err != nil {
				return
			}
			switch e := ev.(type) {
			case xproto.KeyPressEvent:
				if ks, err := xKeycodeToKeysym(conn, e.Detail); err == nil {
					capturedVK.Store(uint32(ks))
					capturedKeyDown.Store(true)
				}
			case xproto.KeyReleaseEvent:
				capturedKeyDown.Store(false)
			}
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	gotKeyDown := false
	var pressedVK uintptr

	for time.Now().Before(deadline) {
		if capturedKeyDown.Load() {
			vk := uintptr(capturedVK.Load())
			if !gotKeyDown {
				gotKeyDown = true
				pressedVK = vk
				logf("捕获: keydown keysym=0x%X", vk)
			}
		} else if gotKeyDown {
			// 松开了
			capturing.Store(false)
			if pressedVK == linuxKeysymEscape { // Esc 取消
				updateTrayStatus("状态：已取消")
				logf("取消自定义快捷键")
				return
			}
			// 修饰键无法作为独立热键（释放即停止录音，且与打字冲突），跳过并继续等待下一个键
			if linuxModifierKeysyms[pressedVK] {
				gotKeyDown = false
				capturedVK.Store(0)
				logf("捕获: 修饰键 0x%X 跳过，继续等待", pressedVK)
				capturing.Store(true) // 恢复捕获模式
				continue
			}
			name := vkToName(pressedVK)
			if name == "" {
				updateTrayStatus("状态：不支持的按键 keysym=0x" + fmt.Sprintf("%X", pressedVK))
				logf("捕获: 不支持的按键 keysym=0x%X", pressedVK)
				return
			}
			c := *cfg()
			c.Key = name
			c.Mod = ""
			saveCfg(c)
			nc := c
			cfgPtr.Store(&nc)
			setHotkeyCode(vkForName(name)) // 同步更新抓键
			updateTrayStatus("状态：快捷键已设为 " + name)
			logf("自定义快捷键设为 %s (keysym=0x%X)", name, pressedVK)
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	capturing.Store(false)
	updateTrayStatus("状态：超时未捕获")
	logf("自定义快捷键超时未捕获")
}
