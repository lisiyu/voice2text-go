//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/atotto/clipboard"
	"golang.org/x/sys/windows"
)

// ---------- 配置（支持自定义语音接口） ----------
type Config struct {
	Key    string `json:"key"`     // space / f8 / rightctrl / capslock / home / end / insert
	Mod    string `json:"mod"`     // "" / ctrl / alt / shift
	HoldMs int    `json:"hold_ms"` // 长按阈值 ms
	API    string `json:"api"`     // 语音转写接口 URL（OpenAI /audio/transcriptions 兼容）
	Model  string `json:"model"`   // 模型名
	APIKey string `json:"api_key"` // 可选：接口需要的 Bearer Token（留空则不携带）
	// 本地常驻 whisper-server（NPU 加速，不依赖任何三方服务；优先于 cli/API）
	WhisperServerExe  string `json:"whisper_server_exe"`  // whisper-server.exe 完整路径（留空则走 cli/API）
	WhisperServerURL  string `json:"whisper_server_url"`  // 常驻服务地址，默认 http://127.0.0.1:8080/inference
	WhisperServerPort int    `json:"whisper_server_port"` // 常驻端口，默认 8080
	// whisper-cli 直转写（本地兜底；设置了就优先于 API）
	WhisperCLI   string `json:"whisper_cli"`   // whisper-cli.exe 完整路径（留空则用 API）
	WhisperModel string `json:"whisper_model"` // ggml 模型完整路径
	Language     string `json:"language"`      // 识别语言：zh / en / ja ... 留空=自动检测
	Prompt       string `json:"prompt"`        // 引导提示词：用于约束输出（如强制简体中文）

	// ---------- 录音时长保护（防止长音频被模型窗口截断 / 超时） ----------
	// Whisper 模型架构上单次只处理 ~30s 音频窗口；更长音频由转写引擎自动切片，
	// 但单段超长语音可能触达 token 上限被截断，且超长录音可能跑满 HTTP 超时。
	// 这里提供：软警告（波形变橙）+ 硬上限（到点自动停止）+ 长音频自动切片转写。
	WarnRecordingSec int  `json:"warn_recording_sec"` // 软警告阈值（秒）；超过则悬浮窗波形变橙提示。0=用默认 30
	MaxRecordingSec  int  `json:"max_recording_sec"`  // 硬上限（秒）；0=不限制，超过自动停止并转写
	SplitLongAudio   bool `json:"split_long_audio"`   // 长音频(>30s)自动按 30s 切片转写，避免模型窗口截断。默认 true

	// 安装器写入：启动时按环境自动选后端（NPU>GPU>CPU），不支持则自动回退。
	AutoBackend bool `json:"auto_backend"` // true=启动时自动探测并选用可用后端

	// 本地转写后端选择：决定运行时用哪个引擎。
	//   "cli"   (默认) = whisper-cli 直转：每次独立进程 → 全新上下文(ctx)，
	//              永不跨请求污染，识别最稳（代价：每次重载模型 ~4s）。
	//   "server"       = 常驻 whisper-server：最快（模型常驻内存），但 whisper.cpp
	//              /inference 默认跨请求沿用上下文，长驻实例一旦被坏结果污染 ctx，
	//              之后所有转写会塌成"啦啦啦"/残句。本程序已对该路径加
	//              condition_on_previous_text=false + 幻觉检测兜底，但仍不如 cli 稳。
	//   "auto"         = 同 "cli"（优先稳）。
	WhisperBackend string `json:"whisper_backend"`
}

// defaultWhisperPort whisper-server 默认端口（全项目统一，避免 8080/19743 混用）
const defaultWhisperPort = 8080

var vkMap = map[string]uintptr{
	"space":      0x20,
	"f8":         0x77,
	"f9":         0x78,
	"leftshift":  0xA0,
	"rightshift": 0xA1,
	"leftctrl":   0xA2,
	"rightctrl":  0xA3,
	"leftalt":    0xA4,
	"rightalt":   0xA5,
	"capslock":   0x14,
	"home":       0x24,
	"end":        0x23,
	"insert":     0x2D,
	"tab":        0x09,
	"enter":      0x0D,
	"delete":     0x2E,
	"left":       0x25,
	"right":      0x27,
	"up":         0x26,
	"down":       0x28,
}

// vkForName 支持预设名 + 任意字母/数字/F1-F24
func vkForName(name string) uintptr {
	name = strings.ToLower(strings.TrimSpace(name))
	if v, ok := vkMap[name]; ok {
		return v
	}
	if len(name) == 1 {
		c := name[0]
		if c >= 'a' && c <= 'z' {
			return uintptr(0x41 + c - 'a')
		}
		if c >= '0' && c <= '9' {
			return uintptr(0x30 + c - '0')
		}
	}
	if len(name) > 1 && name[0] == 'f' {
		var num int
		if _, err := fmt.Sscanf(name[1:], "%d", &num); err == nil && num >= 1 && num <= 24 {
			return uintptr(0x6F + num)
		}
	}
	return 0
}

// vkToName 反向映射
func vkToName(vk uintptr) string {
	for name, v := range vkMap {
		if v == vk {
			return name
		}
	}
	if vk >= 0x41 && vk <= 0x5A {
		return string(rune('A' + vk - 0x41))
	}
	if vk >= 0x30 && vk <= 0x39 {
		return string(rune('0' + vk - 0x30))
	}
	if vk >= 0x70 && vk <= 0x87 {
		return fmt.Sprintf("f%d", vk-0x6F)
	}
	return ""
}

func defaultCfg() Config {
	homeDir, _ := os.UserHomeDir()
	// 强制锁定 NPU 路径，不再为寻找 CPU/GPU 做无效探测
	npuBinDir := filepath.Join(homeDir, ".cache", "lemonade", "bin", "whispercpp", "npu")

	return Config{Key: "space", Mod: "", HoldMs: 300,
		// 锁定 NPU 后端路径
		WhisperServerExe:  filepath.Join(npuBinDir, "whisper-server.exe"),
		WhisperServerURL:  "http://127.0.0.1:8080/inference",
		WhisperServerPort: 8080,
		// 模型加载路径也同步锁定到 NPU 推荐路径
		WhisperModel: filepath.Join(homeDir, "models", "lemonade", "whispercpp", "ggml-large-v3-turbo.bin"),
		// 兜底：仅在 NPU 彻底无法使用时，才尝试这个
		WhisperCLI: filepath.Join(homeDir, ".cache", "lemonade", "bin", "whispercpp", "cpu", "whisper-cli.exe"),
		// 远程 API 预填已移除：本地 NPU 后端已锁定，不再默认配置；需要远程 API 的用户自行填写
		Language:         "",    // 留空=自动检测，支持中英混合；强制中文可填 "zh"
		Prompt:           "以下是语音转写内容，使用简体中文，英文单词保持原文不要翻译，直接输出。",
		WarnRecordingSec: 30,
		MaxRecordingSec:  0,
		SplitLongAudio:   true,
	}
}

func cfgPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "voice2text.json"
	}
	return filepath.Join(filepath.Dir(exe), "voice2text.json")
}

func loadCfg() Config {
	c := defaultCfg()
	b, err := os.ReadFile(cfgPath())
	if err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			logf("配置文件解析失败: %v", err)
		}
	}
	if _, ok := vkMap[c.Key]; !ok {
		if vkForName(c.Key) == 0 {
			c.Key = "space"
		}
	}
	// 注意：不强制回填 API/Model —— 本地 NPU 后端优先，无需默认远程 API；
	// 需要远程 API 转写时由用户自行在配置中填写。
	// 边界校验：防止非法配置值导致逻辑异常
	if c.HoldMs < 50 {
		c.HoldMs = 50 // 最短 50ms，避免误触
	}
	if c.HoldMs > 3000 {
		c.HoldMs = 3000 // 最长 3s，防止用户以为没反应
	}
	if c.WarnRecordingSec <= 0 {
		c.WarnRecordingSec = 30 // 与 defaultCfg 一致：默认 30s
	}
	if c.MaxRecordingSec <= 0 {
		c.MaxRecordingSec = 300 // 默认 300s
	}
	if c.MaxRecordingSec > 3600 {
		c.MaxRecordingSec = 3600 // 上限 1 小时
	}
	return c
}

func saveCfg(c Config) {
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(cfgPath(), b, 0o644)
}

// 线程安全读取：热键循环 / 转写 / 托盘 共用
var cfgPtr atomic.Pointer[Config]

func cfg() *Config { return cfgPtr.Load() }

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

// hookKeyDown 由键盘钩子回调同步设置，hotkeyLoop 读取（无竞态）
var hookKeyDown atomic.Bool
var recordingActive atomic.Bool // 录音进行中标记，钩子据此决定是否拦截热键
var hookVK atomic.Uintptr
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

// hookVK 由 main() 在启动时设置，以及用户切换热键时更新

func isDown(vk uintptr) bool {
	r, _, _ := getAsync.Call(vk)
	return r&0x8000 != 0
}

func modVK(m string) uintptr {
	switch m {
	case "ctrl":
		return 0x11
	case "alt":
		return 0x12
	case "shift":
		return 0x10
	}
	return 0
}

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

// ---------- 状态 ----------
type Status struct {
	mu   sync.Mutex
	text string
}

func (s *Status) set(t string) {
	s.mu.Lock()
	s.text = t
	s.mu.Unlock()
	logf("状态: %s", t)
	// 悬浮窗状态联动：录音/转写时显示，空闲/完成后延时隐藏
	driveOverlay(t)
}

var statusItem *Status // 复用 Status 指针用于 updateStatusItem 兼容

var lastTrayStatus atomic.Value // stores string；防御性的原子读写

func updateStatusItem(s *Status) {
	s.mu.Lock()
	t := s.text
	s.mu.Unlock()
	if v, _ := lastTrayStatus.Load().(string); v == t {
		return // 状态未变化，跳过无意义的托盘更新
	}
	lastTrayStatus.Store(t)
	updateTrayStatus("状态：" + t)
}

// ---------- 悬浮窗状态联动 ----------
// 录音/转写中显示悬浮窗（持续可见，直到结果/失败才延迟隐藏）；空闲隐藏。
var ovHideTimer *time.Timer
var ovHideMu sync.Mutex // H5: 保护 ovHideTimer 的并发访问

func driveOverlay(t string) {
	ovHideMu.Lock()
	defer ovHideMu.Unlock()
	switch {
	case strings.Contains(t, "录音中"):
		if ovHideTimer != nil {
			ovHideTimer.Stop()
		}
		ShowOverlay("录音中", 0x004444EF, stRecord) // 红（均衡器条）
		StartRecordAnim()                        // 启动波形动画
	case strings.Contains(t, "转写中"):
		if ovHideTimer != nil {
			ovHideTimer.Stop()
		}
		ShowOverlay("转写中", 0x00FF840A, stTrans) // 蓝（呼吸点）
	case strings.Contains(t, "已粘贴"):
		ShowOverlay("已粘贴", 0x0059C734, stOK) // 绿
		scheduleHideLocked()
	case strings.Contains(t, "未识别"):
		ShowOverlay("未识别", 0x000A9FFF, stWarn) // 橙
		scheduleHideLocked()
	case strings.Contains(t, "失败") || strings.Contains(t, "忽略") || strings.Contains(t, "错误"):
		ShowOverlay("失败", 0x000A9FFF, stWarn) // 橙
		scheduleHideLocked()
	case strings.Contains(t, "未录到声音"):
		ShowOverlay("未录到", 0x000A9FFF, stWarn) // 橙
		scheduleHideLocked()
	case strings.Contains(t, "上限") || strings.Contains(t, "自动停止"):
		// H3 修复：原本“可达上限自动停止”不匹配任何 case，悬浮窗会卡死在录音波形态不消失
		ShowOverlay("已达上限", 0x000A9FFF, stWarn) // 橙
		scheduleHideLocked()
	case strings.Contains(t, "空闲"):
		HideOverlay()
	}
}

// scheduleHideLocked 在持有 ovHideMu 的情况下调用，2 秒后隐藏悬浮窗
func scheduleHideLocked() {
	if ovHideTimer != nil {
		ovHideTimer.Stop()
	}
	ovHideTimer = time.AfterFunc(2*time.Second, func() {
		HideOverlay()
	})
}

// ---------- 转写 ----------
// 设计原则：用户一旦配置本地常驻 NPU whisper-server，就严格只用本地；
// 绝不允许在本地服务未就绪时静默回退到远程 13305（Lemonade 半死状态会返回
// 固定占位串“优优独播剧场”，正是反复踩坑的根因）。
func transcribe(pcm []byte) (string, error) {
	return transcribeInternal(pcm, true)
}

// transcribeInternal 核心转写。precheck=true 时走完整预检/切片逻辑（对外入口），
// false 用于长音频子段：已在上层做过后端预检，直接转写免去重复健康检查。
func transcribeInternal(pcm []byte, precheck bool) (string, error) {
	c := cfg()
	// 长音频（>30s）按模型窗口切片转写，避免单段超长被 token 上限截断 / 跑满超时。
	if precheck && c.SplitLongAudio && PCMSeconds(pcm) > 30 {
		return transcribeSplit(pcm, c)
	}
	// 选择了本地常驻方案
	if c.WhisperServerExe != "" {
		if precheck && !whisperServerHealthy(c) {
			return "", fmt.Errorf("本地 NPU 转写服务未就绪（/health 不通，端口 %d），请确认 voice2text 已正常启动", orDefaultInt(c.WhisperServerPort, defaultWhisperPort))
		}
		return transcribeServer(pcm, c)
	}
	// 仅配置了本地 cli（无常驻 server）时走 cli 直转
	if c.WhisperCLI != "" {
		return transcribeCLI(pcm, c)
	}
	// 完全没配本地方案才走远程 API 兜底
	if c.API != "" {
		return transcribeAPI(pcm, c)
	}
	return "", fmt.Errorf("未配置任何转写后端（本地 server / cli / API 均为空）")
}

// transcribeSplit 长音频切片转写：每段 <=30s（1s 重叠），逐段转写后拼接。
// Whisper 架构上单次只处理 ~30s 窗口，切片可保证完整覆盖、规避单段 token 截断。
// 递归安全：切片均 <=30s，再次进入 transcribe 不会重复切片。
func transcribeSplit(pcm []byte, c *Config) (string, error) {
	chunks := SplitPCM(pcm, 30, 1)
	if len(chunks) <= 1 {
		return transcribe(chunks[0])
	}
	// 入口预检：server 后端只健康检查一次；若服务未就绪，所有子段都会失败，无需并发轰炸
	if c.WhisperServerExe != "" && !whisperServerHealthy(c) {
		return "", fmt.Errorf("本地 NPU 转写服务未就绪（/health 不通，端口 %d）", orDefaultInt(c.WhisperServerPort, defaultWhisperPort))
	}
	logf("长音频: 总时长 %.1fs，按 30s 切片为 %d 段转写", PCMSeconds(pcm), len(chunks))
	type chunkResult struct {
		text string
		err  error
	}
	results := make([]chunkResult, len(chunks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3) // 最多并行 3 路，避免 HTTP 连接池打满
	for i, ch := range chunks {
		wg.Add(1)
		go func(idx int, chunk []byte) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			t, err := transcribeInternal(chunk, false) // 跳过重复健康检查/切片
			if err != nil {
				logf("长音频第 %d/%d 段转写失败: %v", idx+1, len(chunks), err)
			}
			t = strings.TrimSpace(t)
			results[idx] = chunkResult{text: t, err: err}
		}(i, ch)
	}
	wg.Wait()
	var sb strings.Builder
	var firstErr error
	for i, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("第 %d/%d 段失败: %w", i+1, len(chunks), r.err)
			}
			continue
		}
		if r.text == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(r.text)
	}
	if sb.Len() == 0 {
		if firstErr != nil {
			return "", firstErr
		}
		return "", fmt.Errorf("长音频切片转写后无有效内容")
	}
	if firstErr != nil {
		return sb.String(), fmt.Errorf("部分段转写失败（已返回成功部分）: %w", firstErr)
	}
	return sb.String(), nil
}

// transcribeCLI 调用本地 whisper-cli.exe（纯本地，最快最稳）
// ---------- 本地常驻 whisper-server 管理 ----------
var (
	wsProc *exec.Cmd   // 常驻子进程，退出时清理
	wsMu   sync.Mutex  // 保护 wsProc 的并发访问（watchConfig goroutine 可能重启，主线程退出时也会清理）
)

// startWhisperServer 拉起本地 NPU whisper-server 并等待端口就绪（最多 30s）
func startWhisperServer(c *Config) error {
	if c.WhisperServerExe == "" || c.WhisperModel == "" {
		return fmt.Errorf("未配置 whisper_server_exe / whisper_model")
	}
	// 1) 已健康（模型就绪）则复用
	if whisperServerHealthy(c) {
		logf("whisper-server 健康（/health OK），复用现有实例")
		return nil
	}
	// 2) 端口被占但不健康 = 上次残留的孤儿 whisper-server（父进程被 taskkill 后未清理）。
	//    不盲目复用：孤儿可能 NPU 上下文失效/模型未载完，会导致空格/截断/乱码。清理后起新实例。
	if whisperServerAlive(c) {
		logf("端口被占但 /health 不通（疑似僵尸 whisper-server），清理后重启")
		killStaleWhisperServer()
		time.Sleep(1500 * time.Millisecond)
	}
	port := c.WhisperServerPort
	if port == 0 {
		port = defaultWhisperPort
	}
	// 动态线程数：CPU核心数（whisper.cpp 最佳实践），上限16避免过度调度
	nThreads := runtime.NumCPU()
	if nThreads > 16 {
		nThreads = 16
	}
	args := []string{
		"--host", "127.0.0.1", "--port", fmt.Sprintf("%d", port),
		"-m", c.WhisperModel,
		"-t", fmt.Sprintf("%d", nThreads),
	}
	// language 留空时不传递 -l，让 whisper 自动检测（支持中英混合）
	if c.Language != "" {
		args = append(args, "-l", c.Language)
	}
	cmd := exec.Command(c.WhisperServerExe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 whisper-server 失败: %w", err)
	}
	wsMu.Lock()
	wsProc = cmd
	wsMu.Unlock()
	logf("whisper-server 已启动 pid=%d (port=%d)，等待 /health 就绪...", cmd.Process.Pid, port)

	// 3) 等待 /health 200（NPU 大模型加载慢，给 90s）。比纯 TCP 端口探测可靠：
	//    避免“端口先开/模型后载”时第一次请求拿到空/截断结果。
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if whisperServerHealthy(c) {
			logf("whisper-server 已就绪（/health OK）")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	// 超时仍不健康：杀掉本次起的进程，避免留新孤儿
	wsMu.Lock()
	if wsProc != nil && wsProc.Process != nil {
		_ = wsProc.Process.Kill()
		_, _ = wsProc.Process.Wait()
	}
	wsProc = nil
	wsMu.Unlock()
	return fmt.Errorf("whisper-server 在 90s 内未就绪（/health 不通，NPU 模型加载可能失败）")
}

// restartWhisperServer 杀掉当前 whisper-server 并在后台启动新实例（配置热重载后端变更时使用）。
// 新实例就绪后由 startWhisperServer 内部等待，失败仅记日志不阻断。
func restartWhisperServer(c *Config) {
	wsMu.Lock()
	if wsProc != nil && wsProc.Process != nil {
		logf("restartWhisperServer: 杀掉当前实例 pid=%d", wsProc.Process.Pid)
		_ = wsProc.Process.Kill()
		_, _ = wsProc.Process.Wait()
	}
	wsProc = nil
	wsMu.Unlock()
	if err := startWhisperServer(c); err != nil {
		logf("restartWhisperServer: 启动失败 %v", err)
	} else {
		logf("restartWhisperServer: 新实例已就绪")
	}
}

// whisperServerAlive 探测本地 whisper-server 端口是否存活（仅 TCP，不代表模型已就绪）
func whisperServerAlive(c *Config) bool {
	port := c.WhisperServerPort
	if port == 0 {
		port = defaultWhisperPort
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 800*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// 全局复用 HTTP Client（连接池复用，避免每次新建）
var httpClient = &http.Client{Timeout: 60 * time.Second}

var bufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

var quitCh = make(chan struct{})
var healthClient = &http.Client{Timeout: 2 * time.Second}

// whisperServerHealthy 调 /health 端点验证 whisper-server 模型已加载、可正常服务。
// 比 TCP 端口探测可靠：避免“端口先开/模型后载”或僵尸进程占端口时把请求发给半死服务，
// 拿到空/截断/纯空格结果。该端点不存在(404)时回退 TCP（兼容 lemonade 改版/旧版 server）。
func whisperServerHealthy(c *Config) bool {
	port := c.WhisperServerPort
	if port == 0 {
		port = defaultWhisperPort
	}
	client := healthClient
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == 200 {
		return true
	}
	if resp.StatusCode == 404 {
		// 无 /health 端点：回退 TCP（端口能连即认为就绪）
		return whisperServerAlive(c)
	}
	return false
}

// killStaleWhisperServer 清理残留的 whisper-server.exe。
// 场景：父进程 voice2text 被 taskkill 强杀时，defer 不执行，whisper-server 成孤儿继续占端口，
// 下次启动若盲目复用会拿到异常结果（空格/乱码/截断）。
// 优先用 PID 精确杀，无 PID 时退化为按镜像名杀。
func killStaleWhisperServer() {
	wsMu.Lock()
	pid := 0
	if wsProc != nil && wsProc.Process != nil {
		pid = wsProc.Process.Pid
	}
	wsMu.Unlock()
	if pid != 0 {
		// 精确杀：只杀本实例启动的进程
		cmd := exec.Command("taskkill", "/F", "/PID", fmt.Sprintf("%d", pid))
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd.Run(); err == nil {
			logf("已清理残留 whisper-server pid=%d", pid)
			wsMu.Lock()
			wsProc = nil
			wsMu.Unlock()
			return
		}
	}
	// 退化：PID 不可用（孤儿来自被强杀的旧实例），按镜像名清理
	cmd := exec.Command("taskkill", "/F", "/IM", "whisper-server.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err == nil {
		logf("已清理残留 whisper-server（按镜像名）")
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// transcribeServer 走本地 whisper-server 的 /inference 接口（NPU 常驻，最快）
func transcribeServer(pcm []byte, c *Config) (string, error) {
	wav := WAVBytes(pcm)
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	mw := multipart.NewWriter(buf)
	fw, _ := mw.CreateFormFile("file", "voice.wav")
	fw.Write(wav)
	mw.WriteField("model", orDefault(c.Model, "whisper-large-v3-turbo"))
	// language 留空时不传递，让 whisper 自动检测（支持中英混合）
	if c.Language != "" {
		mw.WriteField("language", c.Language)
	}
	if p := strings.TrimSpace(c.Prompt); p != "" {
		mw.WriteField("prompt", p)
	}
	mw.WriteField("response_format", "json")
	// 关键：禁用跨请求上下文继承，防止前一次转写结果污染后续请求
	// 不加此参数 → 第二次起会产生空格/幻觉/假死
	mw.WriteField("condition_on_previous_text", "false")
	mw.Close()
	body := bytes.Clone(buf.Bytes()) // 深拷贝后才能放回 pool
	bufPool.Put(buf)

	url := c.WhisperServerURL
	if url == "" {
		url = fmt.Sprintf("http://127.0.0.1:%d/inference", orDefaultInt(c.WhisperServerPort, defaultWhisperPort))
	}
	client := httpClient
	const maxRetries = 2
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			logf("whisper-server 请求重试 %d/%d", attempt, maxRetries)
			time.Sleep(500 * time.Millisecond)
		}
		req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("whisper-server 请求失败: %w", err)
			continue // 网络错误重试
		}
		if resp.StatusCode >= 500 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("whisper-server HTTP %d", resp.StatusCode)
			continue // 5xx 重试
		}
		if resp.StatusCode != 200 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return "", fmt.Errorf("whisper-server HTTP %d", resp.StatusCode) // 4xx 不重试
		}
		var out struct {
			Text string `json:"text"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		if err != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return "", err
		}
		resp.Body.Close()
		text := strings.TrimSpace(out.Text)
		// 合并连续空格为单个空格（whisper 模型在静音段产生的幻觉空格）
		text = reMultiSpace.ReplaceAllString(text, " ")
		if suspiciousResult(text) {
			return "", fmt.Errorf("whisper-server 返回异常内容（疑似占位）: %q", text)
		}
		return text, nil
	}
	return "", lastErr
}

func orDefaultInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func transcribeCLI(pcm []byte, c *Config) (string, error) {
	suffix := fmt.Sprintf("_%d", time.Now().UnixNano())
	tmp := filepath.Join(os.TempDir(), "v2t_voice"+suffix+".wav")
	if err := os.WriteFile(tmp, WAVBytes(pcm), 0o644); err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	outBase := filepath.Join(os.TempDir(), "v2t_out"+suffix)
	args := []string{"-m", c.WhisperModel, "-f", tmp, "-nt", "-otxt", "-of", outBase}
	if c.Language != "" {
		args = append(args, "-l", c.Language)
	}
	cmd := exec.Command(c.WhisperCLI, args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("whisper-cli: %w %s", err, string(b))
	}
	txtFile := outBase + ".txt"
	b, err := os.ReadFile(txtFile)
	os.Remove(txtFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// transcribeAPI 走 OpenAI 兼容转写接口（自定义端点）
func transcribeAPI(pcm []byte, c *Config) (string, error) {
	// 连通性预检：避免上游服务挂掉时把垃圾响应当结果粘贴（缓存 10s 避免重复 TCP 探测）
	if err := apiReachableCached(c.API, 10*time.Second); err != nil {
		return "", fmt.Errorf("转写服务未运行/不可达（%s）：%w", c.API, err)
	}
	wav := WAVBytes(pcm)
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	mw := multipart.NewWriter(buf)
	fw, _ := mw.CreateFormFile("file", "voice.wav")
	fw.Write(wav)
	mw.WriteField("model", c.Model)
	if c.Language != "" {
		mw.WriteField("language", c.Language)
	}
	mw.Close()
	body := bytes.Clone(buf.Bytes())
	bufPool.Put(buf)

	req, _ := http.NewRequest("POST", c.API, bytes.NewReader(body))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	client := httpClient
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return "", err
	}
	resp.Body.Close()
	text := strings.TrimSpace(out.Text)
	// 结果合理性校验：上游服务（如 Lemonade）在异常状态下可能回固定占位串，
	// 直接当结果粘贴会误导用户。命中可疑特征则报错，提示用户检查转写服务。
	if suspiciousResult(text) {
		return "", fmt.Errorf("转写服务返回异常内容（疑似占位/未真正识别）：%q", text)
	}
	return text, nil
}

// apiReachable 用极短超时探 TCP 端口，判断转写服务是否在线
func apiReachable(apiURL string) error {
	u, err := url.Parse(apiURL)
	if err != nil {
		return fmt.Errorf("URL 解析失败: %w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("URL 解析失败: 空主机名")
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("tcp", host, 1500*time.Millisecond)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// suspiciousResult 判断转写结果是否疑似上游异常占位（而非真实语音识别）
var reChinese = regexp.MustCompile(`[一-龥]`)
var reMultiSpace = regexp.MustCompile(`\s{2,}`)

var (
	apiReachableMu     sync.Mutex
	apiReachableErr    error
	apiReachableLast   time.Time
	apiReachableTarget string
)

func apiReachableCached(apiURL string, ttl time.Duration) error {
	apiReachableMu.Lock()
	defer apiReachableMu.Unlock()
	if apiReachableTarget == apiURL && time.Since(apiReachableLast) < ttl {
		return apiReachableErr
	}
	apiReachableErr = apiReachable(apiURL)
	apiReachableTarget = apiURL
	apiReachableLast = time.Now()
	return apiReachableErr
}

var suspiciousMarkers = []string{"exclusive", "剧场", "tv", "television", "http://", "https://", "yoyo",
	"优优独播", "yo yo television", "演示", "demo", "示例", "please subscribe", "点赞 订阅"}

func suspiciousResult(t string) bool {
	if t == "" {
		return false
	}
	lower := strings.ToLower(t)
	for _, m := range suspiciousMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	if len([]rune(t)) > 30 && !strings.Contains(t, "，") && !strings.Contains(t, "。") &&
		!strings.Contains(t, "、") && !reChinese.MatchString(t) {
		return true
	}
	return false
}

func doTranscribe(pcm []byte, status *Status) {
	status.set("转写中")
	text, err := transcribe(pcm)
	if err != nil {
		status.set(fmt.Sprintf("转写失败: %v", err))
		return
	}
	if text == "" {
		status.set("未识别到内容")
		return
	}
	var clipErr error
	for retry := 0; retry < 3; retry++ {
		clipErr = clipboard.WriteAll(text)
		if clipErr == nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if clipErr != nil {
		status.set(fmt.Sprintf("写入剪贴板失败: %v", clipErr))
		return
	}
	time.Sleep(30 * time.Millisecond)
	paste()
	// 不再每次转写后重启 whisper-server：
	// condition_on_previous_text=false 已防止上下文污染，重启会导致下次请求等待模型重载（10-30s）
	// 仅在检测到异常结果时才需要重启（由 suspiciousResult 触发错误路径）
	status.set("已粘贴：" + text)
}

// ---------- 日志（持久化文件句柄，避免每次 OpenFile+Close） ----------
var logFile *os.File
var logMu sync.Mutex

func logf(format string, a ...interface{}) {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile == nil {
		var err error
		logFile, err = os.OpenFile(filepath.Join(os.TempDir(), "voice2text.log"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
	}
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
	if _, err := logFile.WriteString(line); err != nil {
		// 日志文件不可写（磁盘满/句柄失效）：降级 stderr，并关闭句柄，
		// 让下次 logf 重新 OpenFile 恢复，避免持续对失效句柄重复 syscall
		fmt.Fprint(os.Stderr, line)
		logFile.Close()
		logFile = nil
	}
	// 不每次 Sync：OS 会自动刷盘，频繁 Sync 会造成不必要的磁盘 IO
}

// ---------- 配置热重载 ----------
func watchConfig() {
	lastMod := time.Time{}
	if st, err := os.Stat(cfgPath()); err == nil {
		lastMod = st.ModTime()
	}
	for {
		time.Sleep(2 * time.Second)
		st, err := os.Stat(cfgPath())
		if err != nil {
			continue
		}
		if st.ModTime() != lastMod {
			lastMod = st.ModTime()
			prev := cfg()
			c := loadCfg()
			cfgPtr.Store(&c)
			hookVK.Store(vkForName(c.Key)) // 配置热重载时同步更新钩子
			logf("配置已热重载: key=%s mod=%s api=%s model=%s lang=%s api_key=%s",
				c.Key, c.Mod, c.API, c.Model, c.Language, boolStr(c.APIKey != ""))
			// 后端相关配置变更 → 后台重启 whisper-server，让新配置生效
			if c.WhisperServerExe != "" &&
				(prev.WhisperServerExe != c.WhisperServerExe ||
					prev.WhisperModel != c.WhisperModel ||
					prev.WhisperServerPort != c.WhisperServerPort ||
					prev.WhisperServerURL != c.WhisperServerURL ||
					prev.Language != c.Language ||
					prev.Prompt != c.Prompt) {
				logf("后端配置已变更，后台重启 whisper-server")
				go restartWhisperServer(&c)
			}
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "已设置"
	}
	return "无"
}

// mutex 句柄（持有到进程退出，防止重复启动）
var mutexHandle windows.Handle

func acquireMutex() bool {
	h, err := windows.CreateMutex(nil, true, windows.StringToUTF16Ptr("Global\\voice2text-single-instance"))
	// ERROR_ALREADY_EXISTS 必须先于通用 err 检查，否则 handle 泄漏
	if err == windows.ERROR_ALREADY_EXISTS {
		_ = windows.CloseHandle(h)
		return false
	}
	if err != nil {
		return false
	}
	mutexHandle = h
	return true
}

// ---------- 自定义快捷键捕获 ----------
var capturing atomic.Bool

// 捕获模式下钩子回调写入的按键信息
var capturedVK atomic.Uint32
var capturedKeyDown atomic.Bool

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
			hookVK.Store(vkForName(name)) // 同步更新钩子拦截的 VK
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

// ---------- 热键轮询 ----------
// hookKeyDown 由键盘钩子回调同步设置（无竞态），hotkeyLoop 只需轮询读取。
func hotkeyLoop(status *Status) {
	pressed := false
	var pressedAt time.Time
	rec := &Recorder{}
	recording := false
	recWarned := false // 本次录音是否已提示"超长"

	for {
		if capturing.Load() { // 捕获模式暂停热键
			time.Sleep(25 * time.Millisecond)
			continue
		}
		c := cfg()
		mv := modVK(c.Mod)
		// hookKeyDown 由钩子回调同步设置，modifier 通过 GetAsyncKeyState 检测
		hit := hookKeyDown.Load() && (mv == 0 || isDown(mv))

		// 录音中：时长保护（软警告变橙 + 硬上限自动停止）
		if recording {
			sec := rec.Elapsed()
			warnSec := c.WarnRecordingSec
			if warnSec <= 0 {
				warnSec = 30
			}
			if sec >= float64(warnSec) {
				SetOverLimit(true)
				if !recWarned {
					recWarned = true
					status.set("录音中(超长)")
				}
			}
			if c.MaxRecordingSec > 0 && sec >= float64(c.MaxRecordingSec) {
				// 达到硬上限：自动停止并转写
				recording = false
				recordingActive.Store(false)
				SetActiveRecorder(nil)
				SetOverLimit(false)
				pcm, err := rec.Stop()
				rec = &Recorder{}
				recWarned = false
				if err != nil {
					status.set(fmt.Sprintf("停止录音失败: %v", err))
				} else if len(pcm) < SR*3/10 {
					status.set("录音过短，忽略")
				} else if IsSilent(pcm) {
					status.set("未录到声音")
				} else {
					status.set("已达上限自动停止")
					go doTranscribe(pcm, status)
				}
				pressed = false
				continue
			}
		}

		if hit {
			if !pressed {
				pressed = true
				pressedAt = time.Now()
			} else if !recording && time.Since(pressedAt).Milliseconds() >= int64(c.HoldMs) {
				recording = true
				recordingActive.Store(true) // 通知钩子：录音已开始，拦截热键
				if err := rec.Start(); err != nil {
					status.set(fmt.Sprintf("录音失败: %v", err))
					recording = false
					recordingActive.Store(false) // 启动失败，恢复放行
				} else {
					SetActiveRecorder(rec) // 暴露给悬浮窗读实时波形
					SetOverLimit(false)
					recWarned = false
					status.set("录音中")
				}
			}
		} else {
			if recording {
				recording = false
				recordingActive.Store(false) // 通知钩子：录音已结束，放行热键
				SetActiveRecorder(nil)       // 停止读波形
				SetOverLimit(false)
				recWarned = false
				pcm, err := rec.Stop()
				rec = &Recorder{}
				if err != nil {
					status.set(fmt.Sprintf("停止录音失败: %v", err))
				} else if len(pcm) < SR*3/10 { // <0.3s 忽略
					status.set("录音过短，忽略")
				} else if IsSilent(pcm) {
					// 录音时长够但能量极低 = 麦克风被静音或未输入，明确提示而非转写静音
					status.set("未录到声音")
				} else {
					go doTranscribe(pcm, status)
				}
			}
			pressed = false
		}
		updateStatusItem(status)
		time.Sleep(25 * time.Millisecond)
	}
}

// ---------- 托盘（自实现，替代 getlantern/systray） ----------
func startTray() {
	logf("onReady 进入（自实现托盘）")
	cb := trayCallbacks{
		onKey: func(k string) {
			c := *cfg()
			c.Key = k
			c.Mod = ""
			saveCfg(c)
			nc := c
			cfgPtr.Store(&nc)
			hookVK.Store(vkForName(k)) // 同步更新钩子拦截的 VK
			updateTrayStatus("状态：快捷键已切换为 " + k)
			logf("热键切换为 %s", k)
		},
		onCustom: func() {
			go captureKey()
		},
		onAPI: func() {
			_ = exec.Command("notepad.exe", cfgPath()).Start()
		},
		onQuit: func() {
			logf("收到退出请求")
			quitTray()
			close(quitCh)
		},
	}
	initTray(cb)
}

func main() {
	// 崩溃捕获：把 panic 栈写到日志，避免静默退出
	defer func() {
		if r := recover(); r != nil {
			logf("PANIC: %v", r)
			// 写更详细的栈到单独文件
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			_ = os.WriteFile(filepath.Join(os.TempDir(), "voice2text_panic.log"),
				[]byte(fmt.Sprintf("%v\n%s", r, string(buf[:n]))), 0o644)
		}
	}()
	defer func() {
		if logFile != nil {
			logFile.Sync()
			logFile.Close()
		}
	}()
	if len(os.Args) > 1 && os.Args[1] == "--save-icon" {
		_ = os.WriteFile(filepath.Join(os.TempDir(), "v2t_icon.png"), genPNGIcon(), 0o644)
		return
	}
	// 安装子命令：探测环境（NPU>GPU>CPU）→ 下载依赖 → 写配置。最小化安装包入口。
	if len(os.Args) > 1 && os.Args[1] == "install" {
		// M5：安装阶段崩溃不应被顶层 recover 吞成 exit 0（历史上静默成功过）。
		// 这里先于顶层 recover 捕获，写栈并以 1 退出，确保失败可见。
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "安装过程崩溃: %v\n", r)
				buf := make([]byte, 4096)
				n := runtime.Stack(buf, false)
				_ = os.WriteFile(filepath.Join(os.TempDir(), "voice2text_panic.log"),
					[]byte(fmt.Sprintf("install: %v\n%s", r, string(buf[:n]))), 0o644)
				os.Exit(1)
			}
		}()
		dir := ""
		manifest := ""
		for i := 2; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--dir":
				if i+1 < len(os.Args) {
					dir = os.Args[i+1]
					i++
				}
			case "--manifest":
				if i+1 < len(os.Args) {
					manifest = os.Args[i+1]
					i++
				}
			}
		}
		if err := runInstall(dir, manifest); err != nil {
			fmt.Fprintf(os.Stderr, "安装失败: %v\n", err)
			os.Exit(1)
		}
		return
	}
	// 单实例锁：防止多个实例抢占热键 / 残留幽灵图标
	if !acquireMutex() {
		os.Exit(0)
	}
	c := loadCfg()
	cfgPtr.Store(&c)
	hookVK.Store(vkForName(c.Key)) // 钩子始终拦截此 VK
	logf("启动：热键=%s mod=%s hold=%dms api=%s", c.Key, c.Mod, c.HoldMs, c.API)

	// 自动选后端（安装器写入 auto_backend 时生效）：按 NPU>GPU>CPU 回退。
	// 启动阶段【只复用本机已存在的 exe】，不临时拉起进程探测（避免重复加载大模型、拖慢启动）。
	// 真正的“运行时探测 + 下载”只在 `install` 子命令做。
	if c.AutoBackend || c.WhisperServerExe == "" {
		if b, ok := pickLocalBackend(defaultManifest); ok && b.ExePath != "" {
			c.WhisperServerExe = b.ExePath
			c.WhisperModel = b.ModelPath
			logf("自动选后端(本地复用): %s", b.Label)
		} else {
			logf("自动选后端：本机未找到可用后端，按现有配置回退 cli/API")
		}
	}

	// 拉起本地常驻 whisper-server（NPU 加速，不依赖任何三方服务）
	if c.WhisperServerExe != "" {
		if err := startWhisperServer(&c); err != nil {
			logf("警告：whisper-server 启动失败，将回退 whisper-cli/远程API：%v", err)
		} else {
			// 退出时清理常驻子进程（Kill+Wait 彻底回收，避免留下新孤儿占用端口）
			defer func() {
				wsMu.Lock()
				defer wsMu.Unlock()
				if wsProc != nil && wsProc.Process != nil {
					_ = wsProc.Process.Kill()
					_, _ = wsProc.Process.Wait()
					logf("已停止常驻 whisper-server")
				}
			}()
		}
	}

	status := &Status{text: "空闲"}
	status.set("空闲")
	go hotkeyLoop(status)
	go watchConfig()
	go configHotkeyLoop() // 兜底：Ctrl+Shift+C 打开配置（托盘菜单失效时也能用）
	go hookThread()       // 低级键盘钩子线程（录音期间拦截热键字符）
	initOverlay()         // 悬浮窗（录音/转写状态可视）
	logf("准备进入自实现托盘循环")
	startTray()
	// 主协程阻塞，保持进程存活（托盘消息循环在 goroutine 中运行）
	<-quitCh
}

// configHotkeyLoop 全局配置热键：Ctrl+Shift+Alt+C 打开配置文件（托盘菜单不可用时的兜底）。
// 用较冷门的四键组合，避免与各种应用的单 Ctrl+Shift+C 冲突。
func configHotkeyLoop() {
	const vkC = 0x43 // C
	const vkCtrl = 0x11
	const vkShift = 0x10
	const vkAlt = 0x12
	for {
		ctrl := isDown(vkCtrl)
		shift := isDown(vkShift)
		alt := isDown(vkAlt)
		c := isDown(vkC)
		if ctrl && shift && alt && c {
			logf("配置热键 Ctrl+Shift+Alt+C 触发，打开配置")
			_ = exec.Command("notepad.exe", cfgPath()).Start()
			time.Sleep(500 * time.Millisecond) // 防重复触发
		}
		time.Sleep(50 * time.Millisecond)
	}
}
