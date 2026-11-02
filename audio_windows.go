//go:build windows

package main

// audio_windows.go - 录音：winmm.dll waveIn 原生 API（纯 Go，无 CGO）
// 共享的 Recorder 结构与 PCM 工具见 audio_common.go。
import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

var (
	winmm                 = syscall.NewLazyDLL("winmm.dll")
	waveInOpen            = winmm.NewProc("waveInOpen")
	waveInClose           = winmm.NewProc("waveInClose")
	waveInPrepareHeader   = winmm.NewProc("waveInPrepareHeader")
	waveInUnprepareHeader = winmm.NewProc("waveInUnprepareHeader")
	waveInAddBuffer       = winmm.NewProc("waveInAddBuffer")
	waveInStart           = winmm.NewProc("waveInStart")
	waveInStop            = winmm.NewProc("waveInStop")
	waveInReset           = winmm.NewProc("waveInReset")
)

const (
	WAVE_MAPPER = uint32(0xFFFFFFFF)
	WHDR_DONE   = 0x1
)

type waveFormatEx struct {
	wFormatTag      uint16
	nChannels       uint16
	nSamplesPerSec  uint32
	nAvgBytesPerSec uint32
	nBlockAlign     uint16
	wBitsPerSample  uint16
	cbSize          uint16
}

type waveHdr struct {
	lpData          uintptr
	dwBufferLength  uint32
	dwBytesRecorded uint32
	dwUser          uintptr
	dwFlags         uint32
	dwLoops         uint32
	lpNext          uintptr
	reserved        uintptr
}

// recorderSys 是 Recorder 的 Windows 专属状态。
type recorderSys struct {
	handle uintptr
	bufs   [][]byte
	hdrs   []*waveHdr
}

// Start 打开麦克风并开始录音
func (r *Recorder) Start() error {
	f := waveFormatEx{
		wFormatTag:      1, // PCM
		nChannels:       1,
		nSamplesPerSec:  SR,
		nAvgBytesPerSec: SR * 2,
		nBlockAlign:     2,
		wBitsPerSample:  16,
	}
	var h uintptr
	r1, _, _ := waveInOpen.Call(
		uintptr(unsafe.Pointer(&h)), uintptr(WAVE_MAPPER),
		uintptr(unsafe.Pointer(&f)), 0, 0, 0,
	)
	if r1 != 0 {
		return fmt.Errorf("waveInOpen failed: %d", r1)
	}
	r.sys.handle = h
	r.running.Store(true)
	r.recStart = time.Now()
	hdrSize := unsafe.Sizeof(waveHdr{})
	for i := 0; i < 8; i++ {
		b := make([]byte, bufBytes)
		// 必须用堆分配的 hdr（指针固定），否则 append 移动底层数组会让 waveIn 持有的指针失效
		hdr := &waveHdr{lpData: uintptr(unsafe.Pointer(&b[0])), dwBufferLength: bufBytes}
		waveInPrepareHeader.Call(h, uintptr(unsafe.Pointer(hdr)), hdrSize)
		waveInAddBuffer.Call(h, uintptr(unsafe.Pointer(hdr)), hdrSize)
		r.sys.bufs = append(r.sys.bufs, b)
		r.sys.hdrs = append(r.sys.hdrs, hdr)
	}
	waveInStart.Call(h)
	go r.readLoop()
	return nil
}

// readLoop 轮询已满的缓冲区，拷出数据并重新投递
func (r *Recorder) readLoop() {
	hdrSize := unsafe.Sizeof(waveHdr{})
	for r.running.Load() {
		for i := range r.sys.hdrs {
			if r.sys.hdrs[i].dwFlags&WHDR_DONE != 0 {
				n := int(r.sys.hdrs[i].dwBytesRecorded)
				if n > 0 {
					r.mu.Lock()
					r.data = append(r.data, r.sys.bufs[i][:n]...)
					r.mu.Unlock()
					// 实时能量：把该缓冲细分为多个子窗口，逐个算 RMS 写入环形缓冲，
					// 这样悬浮窗波形刷新率从 2Hz 提到 ~32Hz（肉眼才看得到跳动）。
					const subN = 4
					step := n / subN
					if step < 4 {
						step = 4
					}
					for s := 0; s+step <= n; s += step {
						r.pushLevel(rms(r.sys.bufs[i][s : s+step]))
					}
				}
				r.sys.hdrs[i].dwBytesRecorded = 0
				r.sys.hdrs[i].dwFlags &^= WHDR_DONE
				waveInAddBuffer.Call(r.sys.handle, uintptr(unsafe.Pointer(r.sys.hdrs[i])), hdrSize)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Stop 停止并返回 16k/16bit/mono PCM 数据
func (r *Recorder) Stop() ([]byte, error) {
	r.running.Store(false)
	waveInStop.Call(r.sys.handle)
	time.Sleep(50 * time.Millisecond) // 让残余缓冲落盘

	// 收尾采集
	for i := range r.sys.hdrs {
		if r.sys.hdrs[i].dwFlags&WHDR_DONE != 0 {
			n := r.sys.hdrs[i].dwBytesRecorded
			if n > 0 {
				r.mu.Lock()
				r.data = append(r.data, r.sys.bufs[i][:n]...)
				r.mu.Unlock()
			}
		}
	}
	// 必须先 Unprepare 所有 header，再 Reset/Close，否则内核多媒体资源泄漏
	hdrSize := unsafe.Sizeof(waveHdr{})
	for i := range r.sys.hdrs {
		waveInUnprepareHeader.Call(r.sys.handle, uintptr(unsafe.Pointer(r.sys.hdrs[i])), hdrSize)
	}
	waveInReset.Call(r.sys.handle)
	waveInClose.Call(r.sys.handle)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data, nil
}
