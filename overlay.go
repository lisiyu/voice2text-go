//go:build windows

package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modMsimg32     = windows.NewLazySystemDLL("msimg32.dll")
	procAlphaBlend = modMsimg32.NewProc("AlphaBlend")
)

// ---------- 悬浮窗（豆包风格） ----------
// 大圆角胶囊：屏幕底部居中，中间麦克风图标，录音时两侧彩色声波扩散。
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
	procArc                   = modGdi32Ov.NewProc("Arc")
	procPie                   = modGdi32Ov.NewProc("Pie")
	procRectangle             = modGdi32Ov.NewProc("Rectangle")
)

// fillGradientV 竖向渐变填充（6条色带，规避 GradientFill 兼容问题）
func fillGradientV(hdc uintptr, x, y, w, h int32, topR, topG, topB, botR, botG, botB uint8) {
	const bands int32 = 8
	bh := h / bands
	if bh < 1 {
		bh = 1
	}
	dcBrush, _, _ := procGetStockObject.Call(18)
	oldBrush, _, _ := procSelectObject.Call(hdc, dcBrush)
	defer procSelectObject.Call(hdc, oldBrush)
	for i := int32(0); i < bands; i++ {
		t := float64(i) / float64(bands-1)
		r := uint8(float64(topR) + t*float64(int(botR)-int(topR)))
		g := uint8(float64(topG) + t*float64(int(botG)-int(topG)))
		b := uint8(float64(topB) + t*float64(int(botB)-int(topB)))
		col := uint32(b)<<16 | uint32(g)<<8 | uint32(r)
		procSetDCBrushColor.Call(hdc, uintptr(col))
		var rc struct{ L, T, R, B int32 }
		rc.L = x
		rc.T = y + i*bh
		rc.R = x + w
		rc.B = y + (i+1)*bh
		if i == bands-1 {
			rc.B = y + h
		}
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), dcBrush)
	}
}

// refreshOverlay 强制重绘
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
	WM_APP_OV_TEXT    = 0x8000 + 1
	WM_APP_OV_SHOW    = 0x8000 + 2
	WM_APP_OV_HIDE    = 0x8000 + 3
	WM_APP_OV_ANIM    = 0x8000 + 4
	LWA_ALPHA         = 0x00000002
	TRANSPARENT       = 1
	SM_CXSCREEN       = 0
	SM_CYSCREEN       = 1
	PS_SOLID          = 0
)

// 悬浮窗尺寸（豆包风格紧凑胶囊）
const (
	ovWidth  = 220
	ovHeight = 64
)

var (
	ovHwnd        windows.Handle
	ovText        string
	ovMu          sync.Mutex
	ovState       int32
	ovAnim        int32
	ovPeak        float64
	ovSmooth      [24]float64
	ovSmoothValid int32
	ovOverLimit   int32
	ovFont        uintptr
	ovFontSmall   uintptr

	// 精致 PNG 图标缓存（HBITMAP + 尺寸）
	iconMic     uintptr
	iconMicW    int32
	iconCheck   uintptr
	iconCheckW  int32
	iconWarn    uintptr
	iconWarnW   int32
)

var iconsOnce sync.Once

const (
	stRecord = 0
	stTrans  = 1
	stOK     = 2
	stWarn   = 3
)

// rgb 转 GDI BGR
func bgr(r, g, b uint8) uint32 {
	return uint32(b)<<16 | uint32(g)<<8 | uint32(r)
}

// ---------- 精致 PNG 图标绘制（AlphaBlend 硬件加速） ----------

// pngDecode 解码 PNG 字节为 RGBA 图像
func pngDecode(data []byte) (*image.RGBA, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	rgba := image.NewRGBA(img.Bounds())
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			rgba.Set(x, y, img.At(x, y))
		}
	}
	return rgba, nil
}

// pngToHBITMAP 把 PNG 字节转成带 alpha 的 HBITMAP，返回 hbitmap 和宽高
// 自动去除圆形图标外的背景（AI生成图标常见白色背景问题）
func pngToHBITMAP(pngData []byte) (uintptr, int32, int32) {
	img, err := pngDecode(pngData)
	if err != nil {
		return 0, 0, 0
	}
	w, h := int32(img.Bounds().Dx()), int32(img.Bounds().Dy())

	// 圆形背景去除：图标是居中的圆形，圆外像素全部设为完全透明
	// 这样不依赖颜色判断，最可靠
	cx, cy := float64(w)/2, float64(h)/2
	radius := float64(w) * 0.42 // 圆形半径略小于一半，保留边缘抗锯齿
	for y := 0; y < int(h); y++ {
		for x := 0; x < int(w); x++ {
			i := (y*int(w) + x) * 4
			dx := float64(x) - cx
			dy := float64(y) - cy
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist > radius {
				// 圆外：完全透明
				img.Pix[i+3] = 0
			} else if dist > radius-2 {
				// 边缘：根据距离渐变 alpha，保留抗锯齿
				edgeAlpha := 1.0 - (dist-(radius-2))/2.0
				if edgeAlpha < 0 {
					edgeAlpha = 0
				}
				img.Pix[i+3] = uint8(float64(img.Pix[i+3]) * edgeAlpha)
			}
		}
	}

	// 创建 DIB section
	var bi struct {
		Size          uint32
		Width, Height int32
		Planes        uint16
		BitCount      uint16
		Compression   uint32
		SizeImage     uint32
		XPels, YPels  int32
		ClrUsed       uint32
		ClrImportant  uint32
	}
	bi.Size = uint32(unsafe.Sizeof(bi))
	bi.Width = w
	bi.Height = -h // top-down
	bi.Planes = 1
	bi.BitCount = 32
	bi.Compression = 0 // BI_RGB

	var bits uintptr
	hbm, _, _ := modGdi32Ov.NewProc("CreateDIBSection").Call(
		0, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbm == 0 {
		return 0, 0, 0
	}
	// 拷贝像素（BGRA 格式，预乘 alpha）
	// bits 是 CreateDIBSection 返回的 GDI 托管内存指针（非 Go 堆，GC 不会回收），
	// 转 unsafe.Pointer 再用 unsafe.Slice 创建切片视图是 Windows GDI 编程的标准做法。
	//nolint:govet // uintptr→unsafe.Pointer 转换在此场景安全（内存由 GDI 管理）
	dst := unsafe.Slice((*byte)(unsafe.Pointer(bits)), int(w*h*4))
	src := img.Pix
	for y := int32(0); y < h; y++ {
		for x := int32(0); x < w; x++ {
			i := (y*w + x) * 4
			r, g, b, a := src[i], src[i+1], src[i+2], src[i+3]
			di := int(i)
			if a == 0 {
				dst[di], dst[di+1], dst[di+2], dst[di+3] = 0, 0, 0, 0
			} else {
				dst[di] = byte(uint16(b) * uint16(a) / 255)
				dst[di+1] = byte(uint16(g) * uint16(a) / 255)
				dst[di+2] = byte(uint16(r) * uint16(a) / 255)
				dst[di+3] = a
			}
		}
	}
	return hbm, w, h
}

// drawPNGIcon 用 AlphaBlend 绘制 PNG 图标（HALFTONE 高质量缩放，自动适配源尺寸）
func drawPNGIcon(hdc uintptr, hbm uintptr, srcW int32, cx, cy, size int32) {
	if hbm == 0 || srcW == 0 {
		return
	}
	memDC, _, _ := modGdi32Ov.NewProc("CreateCompatibleDC").Call(hdc)
	if memDC == 0 {
		return
	}
	oldBmp, _, _ := procSelectObject.Call(memDC, hbm)

	// 开启 HALFTONE 高质量拉伸模式
	setStretchBltMode := modGdi32Ov.NewProc("SetStretchBltMode")
	oldMode, _, _ := setStretchBltMode.Call(hdc, 4) // HALFTONE = 4

	var bf struct {
		BlendOp             byte
		BlendFlags          byte
		SourceConstantAlpha byte
		AlphaFormat         byte
	}
	bf.BlendOp = 0x00
	bf.SourceConstantAlpha = 255
	bf.AlphaFormat = 0x01

	x := cx - size/2
	y := cy - size/2
	ret, _, _ := procAlphaBlend.Call(
		hdc, uintptr(x), uintptr(y), uintptr(size), uintptr(size),
		memDC, 0, 0, uintptr(srcW), uintptr(srcW),
		uintptr(*(*uint32)(unsafe.Pointer(&bf))))
	if ret == 0 {
		logf("[图标绘制] AlphaBlend 失败! hdc=%d hbm=%d srcW=%d size=%d pos=(%d,%d)", hdc, hbm, srcW, size, x, y)
	}

	setStretchBltMode.Call(hdc, oldMode)
	procSelectObject.Call(memDC, oldBmp)
	modGdi32Ov.NewProc("DeleteDC").Call(memDC)
}

// ensureIcons 确保图标已加载（sync.Once 保证只执行一次，天然并发安全）
func ensureIcons() {
	iconsOnce.Do(func() {
		iconMic, iconMicW, _ = pngToHBITMAP(genMicPNG())
		iconCheck, iconCheckW, _ = pngToHBITMAP(genCheckPNG())
		iconWarn, iconWarnW, _ = pngToHBITMAP(genWarnPNG())
		logf("[图标加载] mic hbm=%d w=%d, check hbm=%d w=%d, warn hbm=%d w=%d",
			iconMic, iconMicW, iconCheck, iconCheckW, iconWarn, iconWarnW)
	})
}

// drawMicIcon 绘制麦克风图标（固定大小，无动态效果，清晰锐利）
func drawMicIcon(hdc uintptr, cx, cy, size int32, color uint32, pulse float64) {
	ensureIcons()
	drawPNGIcon(hdc, iconMic, iconMicW, cx, cy, size)
}

// drawCheckIcon 绘制绿色对勾（PNG）
func drawCheckIcon(hdc uintptr, cx, cy, size int32, color uint32) {
	ensureIcons()
	drawPNGIcon(hdc, iconCheck, iconCheckW, cx, cy, size)
}

// drawWarnIcon 绘制橙色感叹号（PNG）
func drawWarnIcon(hdc uintptr, cx, cy, size int32, color uint32) {
	ensureIcons()
	drawPNGIcon(hdc, iconWarn, iconWarnW, cx, cy, size)
}

// drawWaveBars 豆包风格声波：从中间向两侧扩散的彩色条
func drawWaveBars(hdc uintptr, cx, cy, maxH int32, anim int32) {
	lv := CurrentLevels()
	const barN = 10
	const barW = 3
	const gap = 3

	var ratios [barN]float64
	for i := 0; i < barN; i++ {
		idx := len(lv) - barN + i
		if idx < 0 {
			ratios[i] = 0
			continue
		}
		r0 := float64(lv[idx]) / 32767.0 * 3.0
		if r0 > 1 {
			r0 = 1
		}
		if r0 < 0 {
			r0 = 0
		}
		ratios[i] = r0
	}

	ovMu.Lock()
	if ovSmoothValid == 0 {
		for i := 0; i < barN; i++ {
			ovSmooth[i] = ratios[i]
		}
		ovPeak = 0.1
		ovSmoothValid = 1
	} else {
		for i := 0; i < barN; i++ {
			ovSmooth[i] += (ratios[i] - ovSmooth[i]) * 0.4
		}
	}
	var smoothCopy [barN]float64
	copy(smoothCopy[:], ovSmooth[:])
	// 峰值在锁内一次算完并回写，避免锁碎片化
	mx := 0.0
	for i := 0; i < barN; i++ {
		if smoothCopy[i] > mx {
			mx = smoothCopy[i]
		}
	}
	if mx > ovPeak {
		ovPeak = mx
	}
	ovPeak *= 0.9
	if ovPeak < 0.06 {
		ovPeak = 0.06
	}
	peak := ovPeak
	ovMu.Unlock()

	np, _, _ := procGetStockObject.Call(8)
	dcBrush, _, _ := procGetStockObject.Call(18)
	oldPen, _, _ := procSelectObject.Call(hdc, np)
	oldBrush, _, _ := procSelectObject.Call(hdc, dcBrush)

	// 豆包渐变色：蓝→紫→粉
	colors := []uint32{
		bgr(59, 130, 246),  // 蓝
		bgr(99, 102, 241),  // 靛蓝
		bgr(139, 92, 246),  // 紫
		bgr(168, 85, 247),  // 深紫
		bgr(192, 132, 252), // 浅紫
		bgr(217, 70, 239),  // 粉紫
		bgr(236, 72, 153),  // 粉
	}

	pitch := int32(barW + gap)
	minH := float64(4)

	// 左侧6条（从中间向左）
	for i := 0; i < barN/2; i++ {
		ri := barN/2 - 1 - i
		r := smoothCopy[ri] / peak
		if r > 1 {
			r = 1
		}
		bh := minH + r*(float64(maxH)-minH)
		x := cx - 20 - int32(i+1)*pitch
		yTop := cy - int32(bh)/2
		yb := cy + int32(bh)/2
		ci := i * len(colors) / (barN / 2)
		if ci >= len(colors) {
			ci = len(colors) - 1
		}
		procSetDCBrushColor.Call(hdc, uintptr(colors[ci]))
		procRoundRect.Call(hdc, uintptr(x), uintptr(yTop), uintptr(x+barW), uintptr(yb), uintptr(barW), uintptr(barW))
	}
	// 右侧6条（从中间向右）
	for i := 0; i < barN/2; i++ {
		ri := barN/2 + i
		r := smoothCopy[ri] / peak
		if r > 1 {
			r = 1
		}
		bh := minH + r*(float64(maxH)-minH)
		x := cx + 20 + int32(i)*pitch
		yTop := cy - int32(bh)/2
		yb := cy + int32(bh)/2
		ci := (barN/2 - 1 - i) * len(colors) / (barN / 2)
		if ci >= len(colors) {
			ci = len(colors) - 1
		}
		procSetDCBrushColor.Call(hdc, uintptr(colors[ci]))
		procRoundRect.Call(hdc, uintptr(x), uintptr(yTop), uintptr(x+barW), uintptr(yb), uintptr(barW), uintptr(barW))
	}

	procSelectObject.Call(hdc, oldBrush)
	procSelectObject.Call(hdc, oldPen)
}

// drawLoadingDots 转写中：三个点跳动
func drawLoadingDots(hdc uintptr, cx, cy int32, anim int32) {
	np, _, _ := procGetStockObject.Call(8)
	dcBrush, _, _ := procGetStockObject.Call(18)
	oldPen, _, _ := procSelectObject.Call(hdc, np)
	oldBrush, _, _ := procSelectObject.Call(hdc, dcBrush)

	colors := []uint32{bgr(59, 130, 246), bgr(139, 92, 246), bgr(236, 72, 153)}
	for i := 0; i < 3; i++ {
		phase := float64(anim)/8 + float64(i)*0.8
		offset := float64(6) * math.Abs(math.Sin(phase))
		x := cx - 20 + int32(i)*20
		y := cy - int32(offset)
		procSetDCBrushColor.Call(hdc, uintptr(colors[i]))
		procEllipse.Call(hdc, uintptr(x-5), uintptr(y-5), uintptr(x+5), uintptr(y+5))
	}

	procSelectObject.Call(hdc, oldBrush)
	procSelectObject.Call(hdc, oldPen)
}

// overlayWndProc 豆包风格绘制
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
			cy := h / 2

			// 背景：深色渐变胶囊（豆包暗色风）
			fillGradientV(hdc, rc.L, rc.T, w, h, 0x2A, 0x2A, 0x2E, 0x12, 0x12, 0x16)

			// 边框（微光）
			np, _, _ := procGetStockObject.Call(8)
			oldPen, _, _ := procSelectObject.Call(hdc, np)
			_ = oldPen

			ovMu.Lock()
			state := ovState
			anim := ovAnim
			txt := ovText
			ovMu.Unlock()

			// 居中图标位置
			iconCX := w / 2
			iconCY := cy - 4

			switch state {
			case stRecord:
				// 录音：精致麦克风图标 + 两侧声波
				pulse := float64(anim) / 5
				drawMicIcon(hdc, iconCX, iconCY, 26, bgr(236, 72, 153), pulse)
				drawWaveBars(hdc, iconCX, iconCY, 28, anim)
				// 录音计时（底部小字）
				sec := CurrentElapsed()
				mm := int(sec) / 60
				ss := int(sec) % 60
				timeTxt := fmt.Sprintf("%d:%02d", mm, ss)
				procSetBkMode.Call(hdc, TRANSPARENT)
				procSetTextColor.Call(hdc, uintptr(bgr(160, 160, 170)))
				if ovFontSmall == 0 {
					ovFontSmall, _, _ = procCreateFont.Call(
						uintptr(11), 0, 0, 0, 500, 0, 0, 0, 0, 0, 0, 0, 0,
						uintptr(unsafe.Pointer(utf16Ptr("Segoe UI"))))
				}
				if ovFontSmall != 0 {
					of, _, _ := procSelectObject.Call(hdc, ovFontSmall)
					et, _ := windows.UTF16PtrFromString(timeTxt)
					tw := int32(len([]rune(timeTxt)) * 7)
					procTextOut.Call(hdc, uintptr(w/2-tw/2), uintptr(h-16), uintptr(unsafe.Pointer(et)), uintptr(len([]rune(timeTxt))))
					procSelectObject.Call(hdc, of)
				}
				// 超长提示
				ovMu.Lock()
				over := ovOverLimit != 0
				ovMu.Unlock()
				if over {
					procSetTextColor.Call(hdc, uintptr(bgr(249, 115, 22)))
					if ovFontSmall != 0 {
						of, _, _ := procSelectObject.Call(hdc, ovFontSmall)
						capTxt, _ := windows.UTF16PtrFromString("超长·将自动停止")
						procTextOut.Call(hdc, uintptr(8), uintptr(6), uintptr(unsafe.Pointer(capTxt)), uintptr(len([]rune("超长·将自动停止"))))
						procSelectObject.Call(hdc, of)
					}
				}

			case stTrans:
				// 转写中：三个跳动点 + 文字
				drawLoadingDots(hdc, iconCX, iconCY, anim)
				procSetBkMode.Call(hdc, TRANSPARENT)
				procSetTextColor.Call(hdc, uintptr(bgr(200, 200, 210)))
				if ovFont == 0 {
					ovFont, _, _ = procCreateFont.Call(
						uintptr(13), 0, 0, 0, 600, 0, 0, 0, 0, 0, 0, 0, 0,
						uintptr(unsafe.Pointer(utf16Ptr("Segoe UI"))))
				}
				if ovFont != 0 {
					of, _, _ := procSelectObject.Call(hdc, ovFont)
					ptxt, _ := windows.UTF16PtrFromString(txt)
					tw := int32(len([]rune(txt)) * 8)
					procTextOut.Call(hdc, uintptr(w/2-tw/2), uintptr(h-22), uintptr(unsafe.Pointer(ptxt)), uintptr(len([]rune(txt))))
					procSelectObject.Call(hdc, of)
				}

			case stOK:
				// 成功：绿色对勾
				drawCheckIcon(hdc, iconCX, iconCY, 26, bgr(34, 197, 94))
				procSetBkMode.Call(hdc, TRANSPARENT)
				procSetTextColor.Call(hdc, uintptr(bgr(34, 197, 94)))
				if ovFont == 0 {
					ovFont, _, _ = procCreateFont.Call(
						uintptr(13), 0, 0, 0, 600, 0, 0, 0, 0, 0, 0, 0, 0,
						uintptr(unsafe.Pointer(utf16Ptr("Segoe UI"))))
				}
				if ovFont != 0 {
					of, _, _ := procSelectObject.Call(hdc, ovFont)
					ptxt, _ := windows.UTF16PtrFromString(txt)
					tw := int32(len([]rune(txt)) * 8)
					procTextOut.Call(hdc, uintptr(w/2-tw/2), uintptr(h-22), uintptr(unsafe.Pointer(ptxt)), uintptr(len([]rune(txt))))
					procSelectObject.Call(hdc, of)
				}

			case stWarn:
				// 警告：橙色感叹号
				drawWarnIcon(hdc, iconCX, iconCY, 26, bgr(249, 115, 22))
				procSetBkMode.Call(hdc, TRANSPARENT)
				procSetTextColor.Call(hdc, uintptr(bgr(249, 115, 22)))
				if ovFont == 0 {
					ovFont, _, _ = procCreateFont.Call(
						uintptr(13), 0, 0, 0, 600, 0, 0, 0, 0, 0, 0, 0, 0,
						uintptr(unsafe.Pointer(utf16Ptr("Segoe UI"))))
				}
				if ovFont != 0 {
					of, _, _ := procSelectObject.Call(hdc, ovFont)
					ptxt, _ := windows.UTF16PtrFromString(txt)
					tw := int32(len([]rune(txt)) * 8)
					procTextOut.Call(hdc, uintptr(w/2-tw/2), uintptr(h-22), uintptr(unsafe.Pointer(ptxt)), uintptr(len([]rune(txt))))
					procSelectObject.Call(hdc, of)
				}
			}

			procEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		}
		return 0
	case WM_DESTROY:
		// 释放 GDI 对象，避免进程退出时泄漏
		if ovFont != 0 {
			procDeleteObject.Call(ovFont)
			ovFont = 0
		}
		if ovFontSmall != 0 {
			procDeleteObject.Call(ovFontSmall)
			ovFontSmall = 0
		}
		if iconMic != 0 {
			procDeleteObject.Call(iconMic)
			iconMic = 0
		}
		if iconCheck != 0 {
			procDeleteObject.Call(iconCheck)
			iconCheck = 0
		}
		if iconWarn != 0 {
			procDeleteObject.Call(iconWarn)
			iconWarn = 0
		}
		ovHwnd = windows.Handle(0) // 窗口销毁后清空，避免后续 Show/Hide 往失效句柄 PostMessage
		procPostQuitMsgOv.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcOv.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

func intptr(v int) uintptr { return uintptr(v) }

// runOverlay 创建悬浮窗并跑消息循环
func runOverlay() {
	runtime.LockOSThread()
	logf("悬浮窗 goroutine 启动（豆包风格）")
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

	w, h := int32(ovWidth), int32(ovHeight)

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

	procSetLayeredOv.Call(uintptr(hwnd), 0, 240, LWA_ALPHA)
	// Windows 11 DWM 系统圆角（比手动 SetWindowRgn 更平滑，无锯齿）
	var cornerPref uint32 = 2 // DWMWCP_ROUND
	procDwmSetWindowAttribute.Call(uintptr(hwnd), 33, uintptr(unsafe.Pointer(&cornerPref)), 4)
	var backdrop uint32 = 3 // Mica 效果（深色背景更通透）
	procDwmSetWindowAttribute.Call(uintptr(hwnd), 38, uintptr(unsafe.Pointer(&backdrop)), 4)
	procShowWindowOv.Call(uintptr(hwnd), SW_HIDE)
	logf("悬浮窗已创建 hwnd=%d (%dx%d)", hwnd, w, h)

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
			// 豆包风格：屏幕底部居中
			cx, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
			cy, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)
			nx := int32(cx)/2 - w/2
			ny := int32(cy) - h - 120
			ret, _, _ := procSetWindowPos.Call(uintptr(hwnd), uintptr(0), uintptr(nx), uintptr(ny), uintptr(w), uintptr(h),
				0x0010|0x0040)
			logf("overlay SHOW: pos=(%d,%d) ret=%d", nx, ny, ret)
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

func ShowOverlay(text string, color uint32, state int32) {
	ovMu.Lock()
	ovText = text
	ovState = state
	ovMu.Unlock()
	logf("overlay set: state=%d text=%s hwnd=%d", state, text, ovHwnd)
	if ovHwnd != 0 {
		procPostMessageOv.Call(uintptr(ovHwnd), WM_APP_OV_SHOW, 0, 0)
	}
}

func HideOverlay() {
	if ovHwnd != 0 {
		procPostMessageOv.Call(uintptr(ovHwnd), WM_APP_OV_HIDE, 0, 0)
	}
}

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

func StartRecordAnim() {
	gen := recordAnimGeneration.Add(1)
	go func() {
		t := time.NewTicker(33 * time.Millisecond)
		defer t.Stop()
		for {
			ovMu.Lock()
			isRecording := ovState == stRecord
			ovMu.Unlock()
			if recordAnimGeneration.Load() != gen || !isRecording {
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
