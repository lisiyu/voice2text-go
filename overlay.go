//go:build windows

package main

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------- 悬浮窗（录音/转写状态可视） ----------
// 小巧胶囊：跟随当前输入框（焦点控件）上方显示，带彩色状态图标。
// 仅在录音/转写/结果时显示，空闲自动隐藏。

var (
	modUser32Ov = windows.NewLazySystemDLL("user32.dll")
	modGdi32Ov  = windows.NewLazySystemDLL("gdi32.dll")
	modKernelOv = windows.NewLazySystemDLL("kernel32.dll")

	procCreateWindowExOv      = modUser32Ov.NewProc("CreateWindowExW")
	procDefWindowProcOv       = modUser32Ov.NewProc("DefWindowProcW")
	procGetMessageOv          = modUser32Ov.NewProc("GetMessageW")
	procTranslateMsgOv        = modUser32Ov.NewProc("TranslateMessage")
	procDispatchMsgOv         = modUser32Ov.NewProc("DispatchMessageW")
	procPostMessageOv         = modUser32Ov.NewProc("PostMessageW")
	procPostQuitMsgOv         = modUser32Ov.NewProc("PostQuitMessage")
	procShowWindowOv          = modUser32Ov.NewProc("ShowWindow")
	procSetLayeredOv          = modUser32Ov.NewProc("SetLayeredWindowAttributes")
	procGetDC                 = modUser32Ov.NewProc("GetDC")
	procReleaseDC             = modUser32Ov.NewProc("ReleaseDC")
	procRegisterClassExOv     = modUser32Ov.NewProc("RegisterClassExW")
	procGetModuleHandleOv     = modKernelOv.NewProc("GetModuleHandleW")
	procBeginPaint            = modUser32Ov.NewProc("BeginPaint")
	procEndPaint              = modUser32Ov.NewProc("EndPaint")
	procTextOut               = modGdi32Ov.NewProc("TextOutW")
	procCreateFont            = modGdi32Ov.NewProc("CreateFontW")
	procSelectObject          = modGdi32Ov.NewProc("SelectObject")
	procDeleteObject          = modGdi32Ov.NewProc("DeleteObject")
	procSetBkMode             = modGdi32Ov.NewProc("SetBkMode")
	procSetTextColor          = modGdi32Ov.NewProc("SetTextColor")
	procCreateSolidBrush      = modGdi32Ov.NewProc("CreateSolidBrush")
	procSetDCBrushColor       = modGdi32Ov.NewProc("SetDCBrushColor")
	procFillRect              = modUser32Ov.NewProc("FillRect")
	procGetSystemMetrics      = modUser32Ov.NewProc("GetSystemMetrics")
	procGetClientRect         = modUser32Ov.NewProc("GetClientRect")
	procEllipse               = modGdi32Ov.NewProc("Ellipse")
	procRoundRect             = modGdi32Ov.NewProc("RoundRect")
	procCreatePen             = modGdi32Ov.NewProc("CreatePen")
	procMoveToEx              = modGdi32Ov.NewProc("MoveToEx")
	procLineTo                = modGdi32Ov.NewProc("LineTo")
	procGetForegroundWnd      = modUser32Ov.NewProc("GetForegroundWindow")
	procGetFocus              = modUser32Ov.NewProc("GetFocus")
	procGetWindowRect         = modUser32Ov.NewProc("GetWindowRect")
	procSetWindowPos          = modUser32Ov.NewProc("SetWindowPos")
	procSetWindowRgn          = modUser32Ov.NewProc("SetWindowRgn")
	procCreateRoundRectRgn    = modGdi32Ov.NewProc("CreateRoundRectRgn")
	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	procInvalidateRect        = modUser32Ov.NewProc("InvalidateRect")
	procUpdateWindow          = modUser32Ov.NewProc("UpdateWindow")
	procGetStockObject        = modGdi32Ov.NewProc("GetStockObject")
)

// fillGradientV 用横向色带模拟竖向渐变填充矩形（规避 GradientFill 在部分系统缺失的问题）。
// 使用 stock DC_BRUSH + SetDCBrushColor，避免每帧 Create/Delete GDI 对象导致句柄泄漏。
// 颜色参数为 RGB（函数内转 BGR 供 GDI 使用）。
func fillGradientV(hdc uintptr, x, y, w, h int32, topR, topG, topB, botR, botG, botB uint8) {
	const bands int32 = 6 // 6 条色带足够平滑，且大幅减少 GDI 调用
	bh := h / bands
	if bh < 1 {
		bh = 1
	}
	dcBrush, _, _ := procGetStockObject.Call(18) // DC_BRUSH
	oldBrush, _, _ := procSelectObject.Call(hdc, dcBrush)
	defer procSelectObject.Call(hdc, oldBrush)
	for i := int32(0); i < bands; i++ {
		t := float64(i) / float64(bands-1)
		r := uint8(float64(topR) + t*float64(int(botR)-int(topR)))
		g := uint8(float64(topG) + t*float64(int(botG)-int(topG)))
		b := uint8(float64(topB) + t*float64(int(botB)-int(topB)))
		col := uint32(b)<<16 | uint32(g)<<8 | uint32(r) // BGR
		procSetDCBrushColor.Call(hdc, uintptr(col))
		var rc struct{ L, T, R, B int32 }
		rc.L = x
		rc.T = y + i*bh
		rc.R = x + w
		rc.B = y + (i+1)*bh
		if i == bands-1 {
			rc.B = y + h // 最后一条补齐，避免缝隙
		}
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), dcBrush)
	}
}

// refreshOverlay 强制立即重绘悬浮窗（标准做法：InvalidateRect + UpdateWindow）。
// 注意：对 layered 窗口，直接 PostMessage(WM_PAINT) 经常不会触发 BeginPaint 真正重绘，
// 必须用 InvalidateRect/UpdateWindow 才能可靠刷新。
func refreshOverlay(hwnd uintptr) {
	procInvalidateRect.Call(hwnd, 0, 1)
	procUpdateWindow.Call(hwnd)
}

const (
	WS_EX_LAYERED     = 0x00080000
	WS_EX_TOPMOST     = 0x00000008
	WS_EX_TRANSPARENT = 0x00000020
	WS_POPUP          = 0x80000000
	SW_SHOW           = 5
	SW_HIDE           = 0
	WM_PAINT          = 0x000F
	WM_APP_OV_TEXT    = 0x8000 + 1 // 更新文字
	WM_APP_OV_SHOW    = 0x8000 + 2
	WM_APP_OV_HIDE    = 0x8000 + 3
	WM_APP_OV_ANIM    = 0x8000 + 4 // 动画帧（录音闪烁）
	LWA_ALPHA         = 0x00000002
	LWA_COLORKEY      = 0x00000001
	TRANSPARENT       = 1
	OPAQUE            = 2
	SM_CXSCREEN       = 0
	SM_CYSCREEN       = 1
	PS_SOLID          = 0
)

var (
	ovHwnd        windows.Handle
	ovText        string
	ovMu          sync.Mutex
	ovColor       uint32      // 强调色 (RGB)
	ovState       int32       // 0=录音 1=转写 2=成功 3=警告
	ovAnim        int32       // 动画相位（录音闪烁用）
	ovPeak        float64     // 最近能量峰值（归一化 0~1，自适应缩放用）
	ovSmooth      [20]float64 // 波形条高度平滑缓冲（EMA，避免突跳）
	ovSmoothValid int32       // 0=需重置平滑缓冲
	ovOverLimit   int32       // 1=录音已超软警告阈值（波形变橙提示）
	ovFont        uintptr     // 缓存的 Segoe UI 12px 字体，避免每帧 CreateFont
)

// 状态常量
const (
	stRecord = 0
	stTrans  = 1
	stOK     = 2
	stWarn   = 3
)

// overlayWndProc 绘制胶囊面板 + 状态图标
func overlayWndProc(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_PAINT:
		var ps struct {
			Hdc      uintptr
			FErase   int32
			Rect     struct{ L, T, R, B int32 }
			Reserved uintptr
		}
		hdc, _, _ := procBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			var rc struct{ L, T, R, B int32 }
			procGetClientRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rc)))
			h := rc.B - rc.T
			w := rc.R - rc.L

			// 背景：竖向渐变胶囊（深灰→近黑，iOS Voice Memos 暗色风）
			fillGradientV(hdc, rc.L, rc.T, w, h, 0x23, 0x23, 0x25, 0x0A, 0x0A, 0x0C)

			ovMu.Lock()
			state := ovState
			anim := ovAnim
			txt := ovText
			ovMu.Unlock()

			const barN int32 = 20
			const barW int32 = 4
			const barGap int32 = 3
			cy := h / 2

			// 统一选入 stock DC_BRUSH + NULL_PEN，整帧只改颜色，不创建/删除任何 GDI 对象。
			// 之前每帧 Create/Delete 十几个 SolidBrush 导致 USER/GDI 句柄泄漏，是“运行就卡死”的根因。
			np, _, _ := procGetStockObject.Call(8)       // NULL_PEN
			dcBrush, _, _ := procGetStockObject.Call(18) // DC_BRUSH
			oldPen, _, _ := procSelectObject.Call(hdc, np)
			oldBrush, _, _ := procSelectObject.Call(hdc, dcBrush)

			if state == stRecord {
				// iOS Voice Memos 风格：居中圆角胶囊条，右→左滚动（最新在右），EMA 平滑
				lv := CurrentLevels()
				var ratios [20]float64
				for i := int32(0); i < barN; i++ {
					idx := int32(len(lv)) - barN + i
					if idx < 0 {
						ratios[i] = 0 // 无样本处保持基线，波形从右侧“长”出来
						continue
					}
					r0 := float64(lv[idx]) / 32767.0
					if r0 > 1 {
						r0 = 1
					}
					if r0 < 0 {
						r0 = 0
					}
					ratios[i] = r0
				}
				// 平滑插值（参考 voice_waveform 的 _lerp(last,cur,0.3)）
				ovMu.Lock()
				if ovSmoothValid == 0 {
					for i := int32(0); i < barN; i++ {
						ovSmooth[i] = ratios[i]
					}
					ovPeak = 0.12 // 新录音重置峰值，避免沿用旧峰值导致条过短
					ovSmoothValid = 1
				} else {
					for i := int32(0); i < barN; i++ {
						ovSmooth[i] += (ratios[i] - ovSmooth[i]) * 0.3
					}
				}
				ovMu.Unlock()

				// 自适应峰值（小声也有反应）
				ovMu.Lock()
				peak := ovPeak
				ovMu.Unlock()
				mx := 0.0
				for i := int32(0); i < barN; i++ {
					if ovSmooth[i] > mx {
						mx = ovSmooth[i]
					}
				}
				if mx > peak {
					peak = mx
				}
				peak *= 0.92
				if peak < 0.12 {
					peak = 0.12
				}
				ovMu.Lock()
				ovPeak = peak
				ovMu.Unlock()

				minH := float64(4)
				maxH := float64(h - 8)
				pitch := barW + barGap
				totalW := barN*pitch - barGap
				startX := rc.L + (w-totalW)/2

				ovMu.Lock()
				over := ovOverLimit != 0
				ovMu.Unlock()
				if over {
					// 超长提示：波形下移、压矮，顶部留白给提示文字
					maxH = float64(h - 16)
					cy = int32(float64(h) * 0.68)
				}
				barColor := uint32(0x005F37FF) // 红 #FF375F（BGR）
				if over {
					barColor = 0x000A9FFF // 橙
				}
				procSetDCBrushColor.Call(hdc, uintptr(barColor))
				for i := int32(0); i < barN; i++ {
					ri := barN - 1 - i // 右→左：最新在右
					r := ovSmooth[ri] / peak
					if r > 1 {
						r = 1
					}
					bh := minH + r*(maxH-minH)
					if bh < minH {
						bh = minH
					}
					x := startX + i*pitch
					yTop := cy - int32(bh)/2
					yb := cy + int32(bh)/2
					procRoundRect.Call(hdc, uintptr(x), uintptr(yTop), uintptr(x+barW), uintptr(yb), uintptr(barW), uintptr(barW))
				}
				// M4：录音计时（顶部右角小字），让用户知道已录多久 / 距阈值还有多久
				{
					sec := CurrentElapsed()
					mm := int(sec) / 60
					ss := int(sec) % 60
					elapsedTxt := fmt.Sprintf("%d:%02d", mm, ss)
					procSetBkMode.Call(hdc, TRANSPARENT)
					procSetTextColor.Call(hdc, uintptr(0x00AAAAAA)) // 浅灰
					if ovFont == 0 {
						ovFont, _, _ = procCreateFont.Call(
							uintptr(11), 0, 0, 0, 600, 0, 0, 0, 0, 0, 0, 0, 0,
							uintptr(unsafe.Pointer(utf16Ptr("Segoe UI"))))
					}
					if ovFont != 0 {
						of, _, _ := procSelectObject.Call(hdc, ovFont)
						et, _ := windows.UTF16PtrFromString(elapsedTxt)
						ew := int32(len([]rune(elapsedTxt)) * 7) // 估算宽度，~7px/字
						procTextOut.Call(hdc, uintptr(w-ew-6), uintptr(3), uintptr(unsafe.Pointer(et)), uintptr(len([]rune(elapsedTxt))))
						procSelectObject.Call(hdc, of)
					}
				}
				// 超长提示文字（顶部一行小字）：明确告诉用户“已超阈值，将自动停止”
				if over {
					procSetBkMode.Call(hdc, TRANSPARENT)
					procSetTextColor.Call(hdc, uintptr(0x000A9FFF)) // 橙
					if ovFont == 0 {
						ovFont, _, _ = procCreateFont.Call(
							uintptr(11), 0, 0, 0, 600, 0, 0, 0, 0, 0, 0, 0, 0,
							uintptr(unsafe.Pointer(utf16Ptr("Segoe UI"))))
					}
					if ovFont != 0 {
						of, _, _ := procSelectObject.Call(hdc, ovFont)
						capTxt, _ := windows.UTF16PtrFromString("超长·将自动停止")
						procTextOut.Call(hdc, uintptr(6), uintptr(3), uintptr(unsafe.Pointer(capTxt)), uintptr(len([]rune("超长·将自动停止"))))
						procSelectObject.Call(hdc, of)
					}
				}
			} else {
				// 其它状态：状态圆点（呼吸 / 脉冲）+ 文字
				ovMu.Lock()
				ovSmoothValid = 0
				ovMu.Unlock()
				var dotC uint32 = 0x000A9FFF // 橙（默认 / 警告）
				var r int32 = 6
				switch state {
				case stTrans:
					dotC = 0x00FF840A  // 蓝（处理中）
					r = 6 + (anim % 3) // 呼吸微动
				case stOK:
					dotC = 0x0059C734 // 绿（成功）
				}
				dcx := int32(28)
				dcy := cy
				procSetDCBrushColor.Call(hdc, uintptr(dotC))
				procEllipse.Call(hdc, uintptr(dcx-r), uintptr(dcy-r), uintptr(dcx+r), uintptr(dcy+r))
				// 高光点（玻璃质感）
				procSetDCBrushColor.Call(hdc, uintptr(0x00FFFFFF))
				procEllipse.Call(hdc, uintptr(dcx-2), uintptr(dcy-3), uintptr(dcx+1), uintptr(dcy))
			}

			// 文字（非录音态显示在圆点右侧；录音态满宽波形不显示文字）
			if state != stRecord {
				procSetBkMode.Call(hdc, TRANSPARENT)
				procSetTextColor.Call(hdc, uintptr(0x00F2F2F2))
				if ovFont == 0 {
					ovFont, _, _ = procCreateFont.Call(
						uintptr(12), 0, 0, 0, 600, 0, 0, 0, 0, 0, 0, 0, 0,
						uintptr(unsafe.Pointer(utf16Ptr("Segoe UI"))))
				}
				if ovFont != 0 {
					oldFont, _, _ := procSelectObject.Call(hdc, ovFont)
					ptxt, _ := windows.UTF16PtrFromString(txt)
					procTextOut.Call(hdc, uintptr(44), uintptr((h-12)/2), uintptr(unsafe.Pointer(ptxt)), uintptr(len([]rune(txt))))
					procSelectObject.Call(hdc, oldFont)
				}
			}

			// 还原原始画笔/画刷，保持 HDC 干净
			procSelectObject.Call(hdc, oldBrush)
			procSelectObject.Call(hdc, oldPen)
			procEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		}
		return 0
	case WM_DESTROY:
		procPostQuitMsgOv.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcOv.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

func intptr(v int) uintptr { return uintptr(v) }

// findAnchor 返回锚点屏幕矩形，用于把胶囊定位到附近。
// 优先用鼠标光标位置（用户要求"跟随鼠标"），其次退化为焦点窗口。
func findAnchor() (x, y, w, h int32) {
	// 优先：鼠标光标位置作为锚点（用返回值判断成功，而非坐标值）
	var pt struct{ X, Y int32 }
	ret, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	if ret != 0 {
		return pt.X, pt.Y, 1, 1
	}
	// 兜底：焦点窗口
	fg, _, _ := procGetForegroundWnd.Call()
	if fg == 0 {
		return 0, 0, 0, 0
	}
	focus, _, _ := procGetFocus.Call()
	if focus == 0 {
		focus = fg
	}
	var rc struct{ L, T, R, B int32 }
	procGetWindowRect.Call(focus, uintptr(unsafe.Pointer(&rc)))
	if rc.R <= rc.L || rc.B <= rc.T {
		return 0, 0, 0, 0
	}
	return rc.L, rc.T, rc.R - rc.L, rc.B - rc.T
}

// runOverlay 创建悬浮窗并跑消息循环（在 goroutine 中）
func runOverlay() {
	runtime.LockOSThread() // 必须：PostMessage 投递到创建窗口的线程，Go 迁移 goroutine 后 GetMessage 收不到消息
	logf("悬浮窗 goroutine 启动")
	hmod, _, _ := procGetModuleHandleOv.Call(0)
	hInstance := windows.Handle(hmod)
	if hmod == 0 {
		logf("悬浮窗 GetModuleHandle 失败")
		return
	}

	clsName, _ := windows.UTF16PtrFromString("voice2text_overlay_cls")
	var wc wndClassEx
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	wc.LpfnWndProc = windows.NewCallback(overlayWndProc)
	wc.HInstance = hInstance
	wc.LpszClassName = clsName
	wc.HbrBackground = 0

	ret, _, err := procRegisterClassExOv.Call(uintptr(unsafe.Pointer(&wc)))
	if ret == 0 {
		logf("悬浮窗 RegisterClassEx 失败: %v", err)
		return
	}
	logf("悬浮窗 RegisterClassEx 成功")

	w, h := int32(160), int32(36)

	hwnd, _, err := procCreateWindowExOv.Call(
		WS_EX_LAYERED|WS_EX_TOPMOST|WS_EX_TRANSPARENT,
		uintptr(unsafe.Pointer(clsName)),
		uintptr(unsafe.Pointer(utf16Ptr("voice2text_overlay"))),
		WS_POPUP,
		0, 0, uintptr(w), uintptr(h),
		0, 0, uintptr(hInstance), 0)
	if hwnd == 0 {
		logf("悬浮窗 CreateWindowEx 失败: %v", err)
		return
	}
	ovHwnd = windows.Handle(hwnd)

	procSetLayeredOv.Call(uintptr(hwnd), 0, 220, LWA_ALPHA)
	rgn, _, _ := procCreateRoundRectRgn.Call(0, 0, uintptr(w), uintptr(h), uintptr(h/2), uintptr(h/2))
	if rgn != 0 {
		procSetWindowRgn.Call(uintptr(hwnd), rgn, 1)
	}
	var backdrop uint32 = 3 // DWMSBT_TRANSIENTWINDOW
	procDwmSetWindowAttribute.Call(uintptr(hwnd), 38, uintptr(unsafe.Pointer(&backdrop)), 4)
	procShowWindowOv.Call(uintptr(hwnd), SW_HIDE)
	logf("悬浮窗已创建 hwnd=%d", hwnd)

	var msg struct {
		Hwnd    windows.Handle
		Message uint32
		WParam  uintptr
		LParam  uintptr
		Time    uint32
		Pt      struct{ X, Y int32 }
	}
	for {
		r, _, _ := procGetMessageOv.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int(r) == -1 {
			logf("overlay GetMessage 返回错误 %d，退出消息循环", r)
			break
		}
		if r == 0 {
			logf("overlay GetMessage 返回 WM_QUIT，退出消息循环")
			break
		}
		switch msg.Message {
		case WM_APP_OV_SHOW:
			ax, ay, aw, ah := findAnchor()
			var nx, ny int32
			if aw > 0 {
				nx = ax + (aw-w)/2
				ny = ay - h - 8
				if ny < 0 {
					ny = ay + ah + 8
				}
			} else {
				cx, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
				nx = int32(cx)/2 - w/2
				ny = 80
			}
			ret, _, _ := procSetWindowPos.Call(uintptr(hwnd), uintptr(0), uintptr(nx), uintptr(ny), uintptr(w), uintptr(h),
				0x0010|0x0040) // SWP_NOACTIVATE | SWP_SHOWWINDOW
			logf("overlay SHOW: anchor=(%d,%d,%d,%d) -> pos=(%d,%d) SetWindowPos ret=%d", ax, ay, aw, ah, nx, ny, ret)
			refreshOverlay(hwnd)
			continue
		case WM_APP_OV_HIDE:
			procShowWindowOv.Call(uintptr(hwnd), SW_HIDE)
			continue
		case WM_APP_OV_ANIM:
			atomic.AddInt32(&ovAnim, 1)
			refreshOverlay(hwnd)
			continue
		}
		procTranslateMsgOv.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMsgOv.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// ---------- 对外接口 ----------
func initOverlay() {
	go runOverlay()
}

// ShowOverlay 显示悬浮窗并切换到指定状态（state 决定图标，text 决定文字）
func ShowOverlay(text string, color uint32, state int32) {
	ovMu.Lock()
	ovText = text
	ovColor = color
	ovState = state
	ovMu.Unlock()
	logf("overlay set: state=%d text=%s hwnd=%d", state, text, ovHwnd)
	if ovHwnd != 0 {
		procPostMessageOv.Call(uintptr(ovHwnd), WM_APP_OV_SHOW, 0, 0)
	}
}

// HideOverlay 隐藏悬浮窗
func HideOverlay() {
	if ovHwnd != 0 {
		procPostMessageOv.Call(uintptr(ovHwnd), WM_APP_OV_HIDE, 0, 0)
	}
}

// SetOverLimit 设置录音超软警告阈值标记（驱动波形变橙）
func SetOverLimit(v bool) {
	ovMu.Lock()
	if v {
		ovOverLimit = 1
	} else {
		ovOverLimit = 0
	}
	ovMu.Unlock()
}

var recordAnimGeneration atomic.Int64

// StartRecordAnim 启动录音动画（独立 goroutine 定时发 ANIM 消息，驱动波形刷新）
func StartRecordAnim() {
	gen := recordAnimGeneration.Add(1) // 每次调用递增，旧 goroutine 自动失效
	go func() {
		t := time.NewTicker(33 * time.Millisecond) // ~30fps，波形更流畅
		defer t.Stop()
		for {
			// 新一轮动画已启动或录音态已退出，当前 goroutine 作废
			if recordAnimGeneration.Load() != gen || atomic.LoadInt32(&ovState) != stRecord {
				ovMu.Lock()
				ovSmoothValid = 0
				ovMu.Unlock()
				return
			}
			if ovHwnd != 0 {
				procPostMessageOv.Call(uintptr(ovHwnd), WM_APP_OV_ANIM, 0, 0)
			}
			<-t.C
		}
	}()
}
