//go:build windows

package main

// audio.go - 录音：winmm.dll waveIn 原生 API（纯 Go，无 CGO）
import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
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
	SR          = 16000 // 采样率
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

// Recorder 封装 waveIn 录音
type Recorder struct {
	handle   uintptr
	bufs     [][]byte
	hdrs     []*waveHdr
	mu       sync.Mutex
	data     []byte
	running  atomic.Bool
	recStart time.Time // 录音开始时刻（用于时长保护 / 超长提示）

	// 实时能量（RMS）环形缓冲，供悬浮窗绘制波形/声纹
	levels  []int32
	lvIdx   int
	lvCount int
	lvMu    sync.Mutex
	lastRMS int32 // 最近一次 RMS（0~32767）
}

const ovWaveBars = 48 // 悬浮窗波形条数（环形缓冲深度，约 1.5s 历史）

const bufBytes = SR / 4 // 0.125 秒 @16k/16bit/mono = 4000 字节（小缓冲→高频刷新，波形才"动"）

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
	r.handle = h
	r.running.Store(true)
	r.recStart = time.Now()
	hdrSize := unsafe.Sizeof(waveHdr{})
	for i := 0; i < 8; i++ {
		b := make([]byte, bufBytes)
		// 必须用堆分配的 hdr（指针固定），否则 append 移动底层数组会让 waveIn 持有的指针失效
		hdr := &waveHdr{lpData: uintptr(unsafe.Pointer(&b[0])), dwBufferLength: bufBytes}
		waveInPrepareHeader.Call(h, uintptr(unsafe.Pointer(hdr)), hdrSize)
		waveInAddBuffer.Call(h, uintptr(unsafe.Pointer(hdr)), hdrSize)
		r.bufs = append(r.bufs, b)
		r.hdrs = append(r.hdrs, hdr)
	}
	waveInStart.Call(h)
	go r.readLoop()
	return nil
}

// readLoop 轮询已满的缓冲区，拷出数据并重新投递
func (r *Recorder) readLoop() {
	hdrSize := unsafe.Sizeof(waveHdr{})
	for r.running.Load() {
		for i := range r.hdrs {
			if r.hdrs[i].dwFlags&WHDR_DONE != 0 {
				n := int(r.hdrs[i].dwBytesRecorded)
				if n > 0 {
					r.mu.Lock()
					r.data = append(r.data, r.bufs[i][:n]...)
					r.mu.Unlock()
					// 实时能量：把该缓冲细分为多个子窗口，逐个算 RMS 写入环形缓冲，
					// 这样悬浮窗波形刷新率从 2Hz 提到 ~32Hz（肉眼才看得到跳动）。
					const subN = 4
					step := n / subN
					if step < 4 {
						step = 4
					}
					for s := 0; s+step <= n; s += step {
						r.pushLevel(rms(r.bufs[i][s : s+step]))
					}
				}
				r.hdrs[i].dwBytesRecorded = 0
				r.hdrs[i].dwFlags &^= WHDR_DONE
				waveInAddBuffer.Call(r.handle, uintptr(unsafe.Pointer(r.hdrs[i])), hdrSize)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pushLevel 把一次 RMS 采样写入环形缓冲（容量为 ovWaveBars）
func (r *Recorder) pushLevel(v float64) {
	r.lvMu.Lock()
	if r.levels == nil {
		r.levels = make([]int32, ovWaveBars)
	}
	iv := int32(v)
	if iv < 0 {
		iv = 0
	}
	if iv > 32767 {
		iv = 32767
	}
	r.levels[r.lvIdx] = iv
	r.lastRMS = iv
	r.lvIdx = (r.lvIdx + 1) % ovWaveBars
	if r.lvCount < ovWaveBars {
		r.lvCount++
	}
	r.lvMu.Unlock()
}

// Levels 返回最近 ovWaveBars 次 RMS 采样（按时间顺序，旧→新），用于绘制波形。
// 若尚未录满一圈，前 (ovWaveBars-lvCount) 个为 0。
// 每调用返回独立的局部缓冲（48×4=192B），避免包级共享导致跨调用/跨 goroutine 数据覆盖。
func (r *Recorder) Levels() []int32 {
	var buf [ovWaveBars]int32
	r.lvMu.Lock()
	defer r.lvMu.Unlock()
	if r.levels == nil {
		return buf[:]
	}
	if r.lvCount < ovWaveBars {
		copy(buf[ovWaveBars-r.lvCount:], r.levels[:r.lvCount])
		return buf[:]
	}
	// 已满：从最旧开始拷贝
	for i := 0; i < ovWaveBars; i++ {
		buf[i] = r.levels[(r.lvIdx+i)%ovWaveBars]
	}
	return buf[:]
}

// Stop 停止并返回 16k/16bit/mono PCM 数据
func (r *Recorder) Stop() ([]byte, error) {
	r.running.Store(false)
	waveInStop.Call(r.handle)
	time.Sleep(50 * time.Millisecond) // 让残余缓冲落盘

	// 收尾采集
	for i := range r.hdrs {
		if r.hdrs[i].dwFlags&WHDR_DONE != 0 {
			n := r.hdrs[i].dwBytesRecorded
			if n > 0 {
				r.mu.Lock()
				r.data = append(r.data, r.bufs[i][:n]...)
				r.mu.Unlock()
			}
		}
	}
	// 必须先 Unprepare 所有 header，再 Reset/Close，否则内核多媒体资源泄漏
	hdrSize := unsafe.Sizeof(waveHdr{})
	for i := range r.hdrs {
		waveInUnprepareHeader.Call(r.handle, uintptr(unsafe.Pointer(r.hdrs[i])), hdrSize)
	}
	waveInReset.Call(r.handle)
	waveInClose.Call(r.handle)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data, nil
}

// WAVBytes 把 PCM 包装成 WAV 文件字节
func WAVBytes(pcm []byte) []byte {
	const hdr = 44
	out := make([]byte, hdr+len(pcm))
	// RIFF
	copy(out[0:4], "RIFF")
	putU32(out[4:8], uint32(36+len(pcm)))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	putU32(out[16:20], 16)
	putU16(out[20:22], 1)    // PCM
	putU16(out[22:24], 1)    // mono
	putU32(out[24:28], SR)   // sample rate
	putU32(out[28:32], SR*2) // byte rate
	putU16(out[32:34], 2)    // block align
	putU16(out[34:36], 16)   // bits
	copy(out[36:40], "data")
	putU32(out[40:44], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out
}

func putU16(b []byte, v uint16) {
	b[0], b[1] = byte(v), byte(v>>8)
}
func putU32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// rms 计算 16bit PCM 的均方根能量（0~32767），用于判断是否为静音
func rms(pcm []byte) float64 {
	if len(pcm) < 2 {
		return 0
	}
	var sum float64
	n := len(pcm) / 2
	for i := 0; i < n; i++ {
		v := int16(pcm[i*2]) | int16(pcm[i*2+1])<<8
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(n))
}

// IsSilent 判断录音是否为静音（麦克风静音/无输入）。阈值 200 约相当于满量程的 0.6%，
// 正常说话远超此值；系统静音或麦克风未开时录到的全零/底噪会低于此值。
func IsSilent(pcm []byte) bool {
	return rms(pcm) < 200
}

// 当前正在录音的 Recorder（供悬浮窗波形读取实时能量）。录音开始时由 main 设置。
// 使用 atomic.Pointer 消除 data race（main 线程写，overlay 线程读）。
var activeRecorder atomic.Pointer[Recorder]

// SetActiveRecorder 设置当前录音对象（nil 表示停止）
func SetActiveRecorder(r *Recorder) {
	activeRecorder.Store(r)
}

// CurrentLevels 返回当前录音的实时能量序列（无录音时全 0）
// 无录音时返回包级零值缓冲（只读，调用方不会修改），避免每帧 192B 分配。
var zeroLevels [ovWaveBars]int32

func CurrentLevels() []int32 {
	r := activeRecorder.Load()
	if r == nil {
		return zeroLevels[:]
	}
	return r.Levels()
}

// Elapsed 返回当前录音已进行时长（秒）
func (r *Recorder) Elapsed() float64 {
	if r.recStart.IsZero() {
		return 0
	}
	return time.Since(r.recStart).Seconds()
}

// CurrentElapsed 返回当前录音已进行时长（秒，无录音返回 0），供悬浮窗显示进度
func CurrentElapsed() float64 {
	r := activeRecorder.Load()
	if r == nil {
		return 0
	}
	return r.Elapsed()
}

// PCMSeconds 计算 PCM 字节数对应的时长（秒）@16k/16bit/mono
func PCMSeconds(pcm []byte) float64 {
	bytesPerSec := SR * 2
	if bytesPerSec <= 0 {
		return 0
	}
	return float64(len(pcm)) / float64(bytesPerSec)
}

// SplitPCM 把长 PCM 切成 <=chunkSec 秒的片段（相邻片段 overlapSec 重叠，减少词边界截断），
// 返回各片段。用于长音频按模型窗口切片转写，避免单段超长被 token 上限截断。
func SplitPCM(pcm []byte, chunkSec, overlapSec float64) [][]byte {
	bytesPerSec := SR * 2
	if bytesPerSec <= 0 || len(pcm) < 2 {
		return [][]byte{pcm}
	}
	chunkBytes := int(chunkSec * float64(bytesPerSec))
	overlapBytes := int(overlapSec * float64(bytesPerSec))
	if chunkBytes < 2 {
		chunkBytes = 2
	}
	if overlapBytes >= chunkBytes {
		overlapBytes = chunkBytes / 4
	}
	if len(pcm) <= chunkBytes {
		return [][]byte{pcm}
	}
	var out [][]byte
	start := 0
	for start < len(pcm) {
		end := start + chunkBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		// 对齐到偶数（16bit 样本边界）
		if (end-start)%2 != 0 {
			end++
			if end > len(pcm) {
				end = len(pcm)
			}
		}
		out = append(out, pcm[start:end])
		if end >= len(pcm) {
			break
		}
		start = end - overlapBytes
		if start < 0 {
			start = 0
		}
	}
	if len(out) == 0 {
		return [][]byte{pcm}
	}
	return out
}
