//go:build windows

package main

// paste_windows.go - 模拟按键（SendInput）与文本输出（剪贴板 + Ctrl+V）。
// 原 main.go 中的 sendKey/paste，原样移至此；
// emitText 把 doTranscribe 里的“写剪贴板 + 模拟粘贴”序列收拢为平台函数
// （Linux 版 emitText 走 wtype/xdotool，见 paste_linux.go）。
import (
	"time"
	"unsafe"

	"github.com/atotto/clipboard"
)

// sendKey 模拟一次按键（SendInput 替代已过时的 keybd_event）
func sendKey(vk uint16, flags uint32) {
	type keybdInput struct {
		wVk         uint16
		wScan       uint16
		dwFlags     uint32
		time        uint32
		dwExtraInfo uintptr
	}
	type input struct {
		atype uint32
		ki    keybdInput
		_     [8]byte // padding 到 32 字节
	}
	var inp input
	inp.atype = inputKeyboard
	inp.ki.wVk = vk
	inp.ki.dwFlags = flags
	sendInput.Call(1, uintptr(unsafe.Pointer(&inp)), unsafe.Sizeof(inp))
}

func paste() {
	sendKey(0x11, 0) // ctrl down
	time.Sleep(20 * time.Millisecond)
	sendKey(0x56, 0) // v down
	time.Sleep(20 * time.Millisecond)
	sendKey(0x56, keyEventFKeyUp) // v up
	time.Sleep(20 * time.Millisecond)
	sendKey(0x11, keyEventFKeyUp) // ctrl up
}

// emitText 把文本送到当前输入框：先写剪贴板（3 次重试），再模拟 Ctrl+V。
// 调用方（doTranscribe）持有 pasteMu，保证“写剪贴板 + 粘贴”序列不被并发转写交错。
func emitText(text string) error {
	var clipErr error
	for retry := 0; retry < 3; retry++ {
		clipErr = clipboard.WriteAll(text)
		if clipErr == nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if clipErr != nil {
		return clipErr
	}
	time.Sleep(30 * time.Millisecond)
	paste()
	return nil
}
