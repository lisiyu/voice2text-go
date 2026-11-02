//go:build windows

package main

// capture_windows.go - 自定义快捷键捕获（托盘菜单 → 3 秒内按键生效）。
// 原 main.go 中的 captureKey，原样移至此。
import (
	"fmt"
	"time"
)

// captureKey 进入捕获模式：3 秒内按下任意键（松开即生效，Esc 取消）
// 通过键盘钩子回调直接检测，不依赖 GetAsyncKeyState
func captureKey() {
	capturing.Store(true)
	capturedVK.Store(0)
	capturedKeyDown.Store(false)
	updateTrayStatus("状态：请按下新热键（3秒内，Esc取消）")
	logf("进入自定义快捷键捕获")

	deadline := time.Now().Add(3 * time.Second)
	gotKeyDown := false
	var pressedVK uintptr

	for time.Now().Before(deadline) {
		if capturedKeyDown.Load() {
			vk := uintptr(capturedVK.Load())
			if !gotKeyDown {
				gotKeyDown = true
				pressedVK = vk
				logf("捕获: keydown vk=0x%X", vk)
			}
		} else if gotKeyDown {
			// 松开了
			capturing.Store(false)
			if pressedVK == 0x1B { // Esc 取消
				updateTrayStatus("状态：已取消")
				logf("取消自定义快捷键")
				return
			}
			// 修饰键无法作为独立热键（释放即停止录音，且与打字冲突），跳过并继续等待下一个键
			switch pressedVK {
			case 0x10, 0x11, 0x12, 0x14, 0x90, 0x91,
				0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0x5B, 0x5C:
				gotKeyDown = false
				capturedVK.Store(0)
				logf("捕获: 修饰键 0x%X 跳过，继续等待", pressedVK)
				capturing.Store(true) // 恢复捕获模式
				continue
			}
			name := vkToName(pressedVK)
			if name == "" {
				updateTrayStatus("状态：不支持的按键 vk=0x" + fmt.Sprintf("%X", pressedVK))
				logf("捕获: 不支持的按键 vk=0x%X", pressedVK)
				return
			}
			c := *cfg()
			c.Key = name
			c.Mod = ""
			saveCfg(c)
			nc := c
			cfgPtr.Store(&nc)
			setHotkeyCode(vkForName(name)) // 同步更新钩子拦截的 VK
			updateTrayStatus("状态：快捷键已设为 " + name)
			logf("自定义快捷键设为 %s (vk=0x%X)", name, pressedVK)
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	capturing.Store(false)
	updateTrayStatus("状态：超时未捕获")
	logf("自定义快捷键超时未捕获")
}
