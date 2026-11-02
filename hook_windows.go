//go:build windows

package main

// hook_windows.go - Windows 低级键盘钩子（WH_KEYBOARD_LL）。
// 原 main.go 中的 Win32 钩子部分，原样移至此。
import (
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------- Win32 ----------
var (
	user32          = syscall.NewLazyDLL("user32.dll")
	getAsync        = user32.NewProc("GetAsyncKeyState")
	sendInput       = user32.NewProc("SendInput")
	setWinHookEx    = user32.NewProc("SetWindowsHookExW")
	unhookWinHookEx = user32.NewProc("UnhookWindowsHookEx")
	callNextHookEx  = user32.NewProc("CallNextHookEx")
)

// SendInput 常量
const (
	inputKeyboard  = 1
	keyEventFKeyUp = 0x0002
)

const (
	whKeyboardLL = 13
	wmKeyDown    = 0x0100
	wmKeyUp      = 0x0101
	wmSysKeyDown = 0x0104
	wmSysKeyUp   = 0x0105
)

var hookHandle uintptr

type kbdLLHookStruct struct {
	vkCode   uint32
	scanCode uint32
	flags    uint32
	time     uint32
	extra    uintptr
}

// lowLevelKeyboardProc 低级键盘钩子：录音期间拦截热键防止字符灌入输入框；
// 非录音时放行，保持按键原有功能可用。同步设置 hookKeyDown 供 hotkeyLoop 读取。
// 捕获模式下记录任意按键供 captureKey 消费。
func lowLevelKeyboardProc(nCode int, wParam uintptr, lParam uintptr) uintptr {
	if nCode >= 0 {
		kb := (*kbdLLHookStruct)(*(*unsafe.Pointer)(unsafe.Pointer(&lParam)))
		vk := uintptr(kb.vkCode)

		// 捕获模式：记录所有按键事件
		if capturing.Load() {
			switch wParam {
			case wmKeyDown, wmSysKeyDown:
				capturedVK.Store(uint32(vk))
				capturedKeyDown.Store(true)
			case wmKeyUp, wmSysKeyUp:
				capturedKeyDown.Store(false)
			}
		}

		// 正常热键拦截（非捕获模式）
		if hvk := hookVK.Load(); hvk != 0 && !capturing.Load() && vk == hvk {
			switch wParam {
			case wmKeyDown, wmSysKeyDown:
				hookKeyDown.Store(true)
				if recordingActive.Load() {
					return 1 // 录音中：拦截 keydown，防止字符灌入输入框
				}
				// 非录音：放行 keydown，保持按键原有功能
			case wmKeyUp, wmSysKeyUp:
				hookKeyDown.Store(false)
				if recordingActive.Load() {
					return 1 // 录音中：拦截 keyup，保持对称
				}
				// 非录音：放行 keyup
			}
		}
	}
	r, _, _ := callNextHookEx.Call(hookHandle, uintptr(nCode), wParam, lParam)
	return r
}

// hookThread 在带消息泵的线程上安装 LL 键盘钩子（LL 钩子要求安装线程有消息循环，且回调始终在该线程执行）
func hookThread() {
	runtime.LockOSThread() // 必须：Go 调度器会迁移 goroutine，LL 钩子回调必须在安装线程上执行，否则进程被杀
	cb := windows.NewCallback(lowLevelKeyboardProc)
	h, _, err := setWinHookEx.Call(whKeyboardLL, cb, 0, 0)
	if h == 0 {
		logf("安装键盘钩子失败: %v", err)
		return
	}
	hookHandle = h
	logf("键盘钩子已安装 handle=%d", h)
	// 消息泵
	var msg struct {
		HWnd    uintptr
		Message uint32
		WParam  uintptr
		LParam  uintptr
		Time    uint32
		Pt      struct{ X, Y int32 }
	}
	for {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int(ret) == -1 {
			break
		}
		// 不需要 Translate/Dispatch，钩子回调由系统直接调用
	}
	unhookWinHookEx.Call(hookHandle)
}

// startHotkeyLayer 是 main() 调用的平台热键层入口（Windows：LL 钩子线程）。
func startHotkeyLayer() {
	hookThread()
}

// setHotkeyCode 更新钩子拦截的按键码（Windows：直接存原子变量即可）。
func setHotkeyCode(code uintptr) {
	hookVK.Store(code)
}
