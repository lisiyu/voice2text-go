//go:build windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------- Windows API ----------
var (
	modShell32 = windows.NewLazySystemDLL("shell32.dll")
	modUser32  = windows.NewLazySystemDLL("user32.dll")
	modKernel  = windows.NewLazySystemDLL("kernel32.dll")

	procShellNotifyIcon     = modShell32.NewProc("Shell_NotifyIconW")
	procCreatePopupMenu     = modUser32.NewProc("CreatePopupMenu")
	procAppendMenu          = modUser32.NewProc("AppendMenuW")
	procTrackPopupMenu      = modUser32.NewProc("TrackPopupMenu")
	procDestroyMenu         = modUser32.NewProc("DestroyMenu")
	procRegisterClassEx     = modUser32.NewProc("RegisterClassExW")
	procCreateWindowEx      = modUser32.NewProc("CreateWindowExW")
	procDefWindowProc       = modUser32.NewProc("DefWindowProcW")
	procGetMessage          = modUser32.NewProc("GetMessageW")
	procTranslateMessage    = modUser32.NewProc("TranslateMessage")
	procDispatchMessage     = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage     = modUser32.NewProc("PostQuitMessage")
	procLoadIcon            = modUser32.NewProc("LoadIconW")
	procLoadImage           = modUser32.NewProc("LoadImageW")
	procDestroyWindow       = modUser32.NewProc("DestroyWindow")
	procSetForegroundWindow = modUser32.NewProc("SetForegroundWindow")
	procGetModuleHandle     = modKernel.NewProc("GetModuleHandleW")
	procCheckMenuItem       = modUser32.NewProc("CheckMenuItem")
	procGetCursorPos        = modUser32.NewProc("GetCursorPos")
)

const (
	NIM_ADD         = 0x00000000
	NIM_MODIFY      = 0x00000001
	NIM_DELETE      = 0x00000002
	NIF_MESSAGE     = 0x00000001
	NIF_ICON        = 0x00000002
	NIF_TIP         = 0x00000004
	WM_USER         = 0x0400
	WM_TRAYICON     = WM_USER + 1
	WM_COMMAND      = 0x0111
	WM_DESTROY      = 0x0002
	WM_RBUTTONUP    = 0x0205
	WM_LBUTTONUP    = 0x0202
	IDI_APPLICATION = 32512
	HWND_MESSAGE    = 0xFFFFFFFFFFFFFFFF // 消息专用窗口（托盘标准做法）

	// 菜单项 ID
	idStatus    = 1001
	idKeySpace  = 1010
	idKeyF8     = 1011
	idKeyF9     = 1012
	idKeyRCtrl  = 1013
	idKeyCaps   = 1014
	idKeyHome   = 1015
	idKeyEnd    = 1016
	idKeyInsert = 1017
	idCustomKey = 1020
	idAPI       = 1021
	idQuit      = 1999
)

type notifyIconData struct {
	CbSize           uint32
	Hwnd             windows.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
}

// 托盘回调
type trayCallbacks struct {
	onKey    func(string)
	onCustom func()
	onAPI    func() // 编辑配置（记事本打开 voice2text.json 本体）
	onQuit   func()
}

var trayCB trayCallbacks
var hwndNotify windows.Handle
var statusText string
var mu sync.Mutex

func setTrayStatus(t string) {
	mu.Lock()
	statusText = t
	mu.Unlock()
}

// 窗口过程
func wndProc(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_TRAYICON:
		if lParam == WM_RBUTTONUP || lParam == WM_LBUTTONUP {
			showTrayMenu(hwnd)
		}
	case WM_COMMAND:
		id := uintptr(wParam) & 0xFFFF
		handleMenu(id)
	case WM_DESTROY:
		procPostQuitMessage.Call(0)
	}
	ret, _, _ := procDefWindowProc.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return ret
}

func utf16Ptr(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func handleMenu(id uintptr) {
	switch id {
	case idKeySpace, idKeyF8, idKeyF9, idKeyRCtrl, idKeyCaps, idKeyHome, idKeyEnd, idKeyInsert:
		m := map[uintptr]string{
			idKeySpace: "space", idKeyF8: "f8", idKeyF9: "f9",
			idKeyRCtrl: "rightctrl", idKeyCaps: "capslock", idKeyHome: "home",
			idKeyEnd: "end", idKeyInsert: "insert",
		}
		if trayCB.onKey != nil {
			trayCB.onKey(m[id])
		}
	case idCustomKey:
		if trayCB.onCustom != nil {
			trayCB.onCustom()
		}
	case idAPI:
		if trayCB.onAPI != nil {
			trayCB.onAPI()
		}
	case idQuit:
		if trayCB.onQuit != nil {
			trayCB.onQuit()
		}
	}
}

func showTrayMenu(hwnd windows.Handle) {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	mu.Lock()
	st := statusText
	mu.Unlock()
	procAppendMenu.Call(hMenu, 0, idStatus, uintptr(unsafe.Pointer(utf16Ptr("状态："+st))))
	procAppendMenu.Call(hMenu, 0x0800, 0, 0) // MF_SEPARATOR
	procAppendMenu.Call(hMenu, 0, idKeySpace, uintptr(unsafe.Pointer(utf16Ptr("空格 (Space)"))))
	procAppendMenu.Call(hMenu, 0, idKeyF8, uintptr(unsafe.Pointer(utf16Ptr("F8"))))
	procAppendMenu.Call(hMenu, 0, idKeyF9, uintptr(unsafe.Pointer(utf16Ptr("F9"))))
	procAppendMenu.Call(hMenu, 0, idKeyRCtrl, uintptr(unsafe.Pointer(utf16Ptr("右 Ctrl"))))
	procAppendMenu.Call(hMenu, 0, idKeyCaps, uintptr(unsafe.Pointer(utf16Ptr("CapsLock"))))
	procAppendMenu.Call(hMenu, 0, idKeyHome, uintptr(unsafe.Pointer(utf16Ptr("Home"))))
	procAppendMenu.Call(hMenu, 0, idKeyEnd, uintptr(unsafe.Pointer(utf16Ptr("End"))))
	procAppendMenu.Call(hMenu, 0, idKeyInsert, uintptr(unsafe.Pointer(utf16Ptr("Insert"))))
	procAppendMenu.Call(hMenu, 0, idCustomKey, uintptr(unsafe.Pointer(utf16Ptr("自定义快捷键..."))))
	procAppendMenu.Call(hMenu, 0x0800, 0, 0) // separator
	procAppendMenu.Call(hMenu, 0, idAPI, uintptr(unsafe.Pointer(utf16Ptr("编辑配置 (记事本)..."))))
	procAppendMenu.Call(hMenu, 0x0800, 0, 0) // separator
	procAppendMenu.Call(hMenu, 0, idQuit, uintptr(unsafe.Pointer(utf16Ptr("退出"))))

	// 高亮当前热键
	cur := currentKeyName()
	highlight := map[string]uintptr{
		"space": idKeySpace, "f8": idKeyF8, "f9": idKeyF9, "rightctrl": idKeyRCtrl,
		"capslock": idKeyCaps, "home": idKeyHome, "end": idKeyEnd, "insert": idKeyInsert,
	}
	if id, ok := highlight[cur]; ok {
		procCheckMenuItem.Call(hMenu, id, 0x0008) // MF_BYCOMMAND|MF_CHECKED
	}

	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(hwnd))
	// TPM_RIGHTBUTTON=0x0002（由 wndProc 异步接收 WM_COMMAND 处理菜单点击，避免双重触发）
	procTrackPopupMenu.Call(hMenu, 0x0002, uintptr(pt.X), uintptr(pt.Y), 0, uintptr(hwnd), 0)
	procDestroyMenu.Call(hMenu)
}

func currentKeyName() string {
	c := cfg()
	if c == nil {
		return "space"
	}
	return c.Key
}

// WNDCLASSEXW 与 C 布局完全一致（含 hIconSm）
type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

// loadAppIcon 加载自绘麦克风图标为 HICON（PNG→ICO→LoadImageW，Win10+ 支持 PNG-in-ICO）；
// 任一步失败回退系统默认 IDI_APPLICATION，保证托盘始终有图标。
func loadAppIcon() windows.Handle {
	ico := genICOIcon()
	tmp := filepath.Join(os.TempDir(), "voice2text_tray.ico")
	if err := os.WriteFile(tmp, ico, 0o644); err == nil {
		// IMAGE_ICON=1, LR_LOADFROMFILE=0x10
		h, _, _ := procLoadImage.Call(0, uintptr(unsafe.Pointer(utf16Ptr(tmp))), 1, 0, 0, 0x10)
		if h != 0 {
			logf("托盘图标：自绘麦克风(ico=%s)", tmp)
			return windows.Handle(h)
		}
		logf("托盘图标：LoadImageW 失败，回退系统默认")
	} else {
		logf("托盘图标：写临时 ico 失败 %v，回退系统默认", err)
	}
	h, _, _ := procLoadIcon.Call(0, uintptr(IDI_APPLICATION))
	return windows.Handle(h)
}

// ---------- 托盘主循环 ----------
func runTray() {
	runtime.LockOSThread() // 必须：窗口消息循环必须在创建窗口的 OS 线程上执行
	hmod, _, _ := procGetModuleHandle.Call(0)
	hInstance := windows.Handle(hmod)

	className, _ := windows.UTF16PtrFromString("voice2text_tray_cls")
	var wc wndClassEx
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	wc.LpfnWndProc = windows.NewCallback(wndProc)
	wc.HInstance = hInstance
	wc.LpszClassName = className
	rIcon, _, _ := procLoadIcon.Call(0, uintptr(IDI_APPLICATION))
	wc.HIcon = windows.Handle(rIcon)

	ret, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	if ret == 0 {
		logf("RegisterClassEx 失败: %v", err)
	} else {
		logf("RegisterClassEx 成功")
	}

	hwnd, _, err2 := procCreateWindowEx.Call(
		uintptr(0),
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("voice2text"))),
		uintptr(0), // WS_OVERLAPPED（隐藏，仅作托盘通知接收窗口）
		0, 0, 0, 0,
		0, // hWndParent = NULL（桌面，最通用）
		0, // hMenu = 无菜单
		uintptr(hInstance),
		0)
	hwndNotify = windows.Handle(hwnd)
	if hwnd == 0 {
		logf("CreateWindowEx 失败: %v（托盘可能不可用，但热键/配置热键仍可用）", err2)
	} else {
		logf("CreateWindowEx 成功 hwnd=%d", hwnd)
	}

	// M2：优先用自绘麦克风图标（PNG→ICO→LoadImageW）；失败回退系统默认
	hIcon := loadAppIcon()

	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.Hwnd = hwndNotify
	nid.UID = 1
	nid.UCallbackMessage = WM_TRAYICON
	tip, _ := windows.UTF16FromString("语音输入 voice2text")
	if len(tip) > 128 {
		tip = tip[:128]
	}
	copy(nid.SzTip[:], tip)

	if hwndNotify != 0 {
		// 先带图标尝试
		nid.UFlags = NIF_MESSAGE | NIF_ICON | NIF_TIP
		nid.HIcon = windows.Handle(hIcon)
		r, _, err := procShellNotifyIcon.Call(NIM_ADD, uintptr(unsafe.Pointer(&nid)))
		if r == 0 {
			logf("Shell_NotifyIcon ADD(带图标) 失败: %v，降级无图标重试", err)
			nid.UFlags = NIF_MESSAGE | NIF_TIP
			nid.HIcon = 0
			r2, _, err2 := procShellNotifyIcon.Call(NIM_ADD, uintptr(unsafe.Pointer(&nid)))
			if r2 == 0 {
				logf("Shell_NotifyIcon ADD(无图标) 仍失败: %v（托盘不可用，但热键/配置热键仍可用）", err2)
			} else {
				logf("托盘已注册(无图标), hwnd=%d", hwnd)
			}
		} else {
			logf("托盘已注册(带图标), hwnd=%d", hwnd)
		}
	} else {
		logf("hwnd 无效，跳过 Shell_NotifyIcon")
	}

	// 消息循环（即使托盘注册失败也继续跑，保证进程存活、热键兜底可用）
	var msg struct {
		Hwnd    windows.Handle
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
		if ret == 0 { // WM_QUIT
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
	procShellNotifyIcon.Call(NIM_DELETE, uintptr(unsafe.Pointer(&nid)))
}

// ---------- 对外接口（替代 systray） ----------
func initTray(cb trayCallbacks) {
	trayCB = cb
	go runTray()
}

func updateTrayStatus(t string) {
	setTrayStatus(t)
}

func quitTray() {
	if hwndNotify != 0 {
		procDestroyWindow.Call(uintptr(hwndNotify))
	}
}
