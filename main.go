// main.go - 共享逻辑（平台无关）。
// 平台专属实现见 *_windows.go / *_linux.go：
// 热键钩子/抓键、按键码映射、录音、托盘、悬浮窗、
// 粘贴输出、单实例锁、api_key 编解码、子进程属性。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
	"time"
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
	WhisperServerURL  string `json:"whisper_server_url"`  // 常驻服务地址，默认 http://127.0.0.1:<平台端口>/inference
	WhisperServerPort int    `json:"whisper_server_port"` // 常驻端口，默认见 defaultWhisperPort（Windows 8080 / Linux 18080）
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

// defaultWhisperPort whisper-server 默认端口，按平台定义
// （见 platform_windows.go / platform_linux.go）：
// Windows 保持 8080；Linux 用 18080，避开本机常驻的 llama-server
// （llama.cpp server 默认占 8080，且它的 /health 也回 200 + {"status":"ok"}，
// 曾导致探活误判为 whisper-server 而复用错服务，见 whisperServerHealthy）。

// 按键名 ↔ 平台按键码的映射见 keys_windows.go / keys_linux.go：
// Windows 用 VK 码，Linux 用 X11 keysym；名字集合保持一致
// （space/f8/f9/左右ctrl/shift/alt、capslock、home、end、insert 等）。
// vkForName/vkToName 由平台文件提供，共享代码只按名字调用。

func defaultCfg() Config {
	// 模型与语音服务默认与软件安装在同一目录（<exe 所在目录>\bin\），
	// 与 install.ps1 的落地布局、backendDir("") 的解析保持一致。
	// 不再硬编码 lemonade 缓存路径：旧默认指向用户机器上可能根本不存在的
	// 路径，会导致录音正常、最后一步转写报"服务未就绪"失败。
	binDir := backendDir("")
	// NPU>GPU>CPU>通用：已存在哪个用哪个；都不存在时给通用名（安装器按机器类型下载对应后端）。
	// 文件名经 platformExeName 换算：Windows 带 .exe 后缀，Linux 不带。
	serverExe := filepath.Join(binDir, platformExeName("whisper-server.exe"))
	for _, name := range []string{"whisper-server-npu.exe", "whisper-server-vulkan.exe", "whisper-server-cpu.exe"} {
		if fileExists(filepath.Join(binDir, platformExeName(name))) {
			serverExe = filepath.Join(binDir, platformExeName(name))
			break
		}
	}

	return Config{Key: "space", Mod: "", HoldMs: 300,
		WhisperServerExe:  serverExe,
		WhisperServerURL:  fmt.Sprintf("http://127.0.0.1:%d/inference", defaultWhisperPort),
		WhisperServerPort: defaultWhisperPort,
		// 模型与语音服务同目录：安装器下载到 bin/ 的 ggml-large-v3-turbo.bin
		WhisperModel: filepath.Join(binDir, "ggml-large-v3-turbo.bin"),
		// cli 兜底同样走同目录
		WhisperCLI: filepath.Join(binDir, platformExeName("whisper-cli.exe")),
		// 远程 API 预填已移除：本地后端优先，无需默认远程 API；需要远程 API 的用户自行填写
		Language:         "", // 留空=自动检测，支持中英混合；强制中文可填 "zh"
		Prompt:           "以下是语音转写内容，使用简体中文，英文单词保持原文不要翻译，直接输出。",
		WarnRecordingSec: 30,
		MaxRecordingSec:  0,
		SplitLongAudio:   true,
		AutoBackend:      true, // 启动时自动探测并复用本机已存在的后端（与安装器写入一致）
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
	// api_key 落盘格式由平台编解码函数处理
	// （Windows：DPAPI 加密；Linux：恒等透传，只调本地模型）；
	// 历史明文配置原样兼容。
	c.APIKey = decodeAPIKeyForLoad(c.APIKey)
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
	// api_key 经平台编码后再落盘（encodeAPIKeyForSave 内处理幂等与失败回退）。
	// c 为值拷贝，不会污染调用方的内存配置。
	c.APIKey = encodeAPIKeyForSave(c.APIKey)
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(cfgPath(), b, 0o644)
}

// 线程安全读取：热键循环 / 转写 / 托盘 共用
var cfgPtr atomic.Pointer[Config]

func cfg() *Config { return cfgPtr.Load() }

// ---------- 热键状态（平台热键层与 hotkeyLoop 共享） ----------
// hookKeyDown 由平台热键层设置（Windows：LL 钩子回调；
// Linux X11：抓键事件循环；Linux Wayland：socket 脉冲），hotkeyLoop 读取。
var hookKeyDown atomic.Bool
var recordingActive atomic.Bool // 录音进行中标记（Windows 钩子据此决定是否拦截热键）
var hookVK atomic.Uintptr       // 当前热键码（Windows：VK；Linux：X11 keysym）

// toggleMode：Wayland 模式为 true，hotkeyLoop 改走 toggle 语义
// （hookKeyDown 上升沿启停录音，不按住/松开语义）。
// Windows 与 Linux X11 下保持 false，走原有按住录音逻辑。
var toggleMode bool

// 捕获模式下平台捕获层写入的按键信息（captureKey 消费）。
var capturing atomic.Bool
var capturedVK atomic.Uint32
var capturedKeyDown atomic.Bool

// 平台热键层入口（见 hook_windows.go / hook_linux.go）：
// Windows 起 LL 钩子线程；Linux X11 起抓键循环，Wayland 起 socket 监听。
// setHotkeyCode 更新热键码（Linux X11 下同时通知抓键循环重新抓键）。

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
	case strings.Contains(t, "已复制到剪贴板"):
		ShowOverlay("已复制", 0x0059C734, stOK) // 绿：文本已在剪贴板，用户按 Ctrl+V 即可
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
	wsProc *exec.Cmd  // 常驻子进程，退出时清理
	wsMu   sync.Mutex // 保护 wsProc 的并发访问（watchConfig goroutine 可能重启，主线程退出时也会清理）
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
	cmd.SysProcAttr = hiddenProcAttr()
	// 子进程输出重定向到日志文件（默认 /dev/null 时 server 崩溃/异常退出
	// 无任何痕迹，无法定位；日志只追加，不影响转写行为）。
	if lf, err := os.OpenFile(filepath.Join(os.TempDir(), "whisper-server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		cmd.Stdout = lf
		cmd.Stderr = lf
		defer lf.Close()
	} else {
		logf("whisper-server 日志文件打不开（继续用默认输出）: %v", err)
	}
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
// 注意：不能只认 /health 200——llama-server 的 /health 同样回 200 + {"status":"ok"}，
// 且它默认占 8080。必须再看 Server 头：whisper.cpp 发 "Server: whisper.cpp"，
// llama.cpp 发 "Server: llama.cpp"。Server 头存在但不含 whisper 时直接判否；
// Server 头缺失时沿用旧行为（200 即健康），避免未来 whisper 版本改头导致复用失效。
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
		if srv := resp.Header.Get("Server"); srv != "" && !strings.Contains(strings.ToLower(srv), "whisper") {
			logf("whisperServerHealthy: 端口 %d 的 /health 200 但 Server 头为 %q，非 whisper-server，不复用", port, srv)
			return false
		}
		return true
	}
	if resp.StatusCode == 404 {
		// 无 /health 端点：回退 TCP（端口能连即认为就绪）
		return whisperServerAlive(c)
	}
	return false
}

// killStaleWhisperServer 清理残留 whisper-server（见 kill_windows.go / kill_linux.go）。
// 场景：父进程被强杀时 defer 不执行，whisper-server 成孤儿继续占端口，
// 下次启动若盲目复用会拿到异常结果（空格/乱码/截断）。
// 优先用 PID 精确杀，无 PID 时退化为按名杀。

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// buildWavMultipart 构造转写请求的 multipart body。
// 所有写入错误（CreateFormFile/Write/WriteField/Close）都返回 error，
// 调用方不得忽略——否则会发出截断/畸形请求，服务端 400 会被误报为"转写失败"。
func buildWavMultipart(wav []byte, fields [][2]string) (body []byte, contentType string, err error) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	mw := multipart.NewWriter(buf)
	fw, err := mw.CreateFormFile("file", "voice.wav")
	if err != nil {
		return nil, "", fmt.Errorf("构造 multipart 失败: %w", err)
	}
	if _, err := fw.Write(wav); err != nil {
		return nil, "", fmt.Errorf("写入音频数据失败: %w", err)
	}
	for _, kv := range fields {
		if err := mw.WriteField(kv[0], kv[1]); err != nil {
			return nil, "", fmt.Errorf("写入表单字段 %s 失败: %w", kv[0], err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", fmt.Errorf("multipart 收尾失败: %w", err)
	}
	// 深拷贝后才能把 buf 放回 pool
	return bytes.Clone(buf.Bytes()), mw.FormDataContentType(), nil
}

// transcribeServer 走本地 whisper-server 的 /inference 接口（NPU 常驻，最快）
func transcribeServer(pcm []byte, c *Config) (string, error) {
	wav := WAVBytes(pcm)
	fields := [][2]string{
		{"model", orDefault(c.Model, "whisper-large-v3-turbo")},
		{"response_format", "json"},
		// 关键：禁用跨请求上下文继承，防止前一次转写结果污染后续请求
		// 不加此参数 → 第二次起会产生空格/幻觉/假死
		{"condition_on_previous_text", "false"},
	}
	// language 留空时不传递，让 whisper 自动检测（支持中英混合）
	if c.Language != "" {
		fields = append(fields, [2]string{"language", c.Language})
	}
	if p := strings.TrimSpace(c.Prompt); p != "" {
		fields = append(fields, [2]string{"prompt", p})
	}
	body, contentType, err := buildWavMultipart(wav, fields)
	if err != nil {
		return "", err
	}

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
		req.Header.Set("Content-Type", contentType)
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
	fields := [][2]string{
		{"model", c.Model},
	}
	if c.Language != "" {
		fields = append(fields, [2]string{"language", c.Language})
	}
	body, contentType, err := buildWavMultipart(wav, fields)
	if err != nil {
		return "", err
	}

	req, _ := http.NewRequest("POST", c.API, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
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

// reASCIIPunct 英文标点白名单：正常英文转写通常携带标点；
// 仅当长句既无中文标点/中文字符、又无英文标点时，才判为疑似占位。
var reASCIIPunct = regexp.MustCompile(`[,.!?;:'"()\[\]-]`)

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
		!strings.Contains(t, "、") && !reChinese.MatchString(t) && !reASCIIPunct.MatchString(t) {
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
	// 串行化文本输出整个序列：并发转写时两个 goroutine 的
	// 输出若交错，A 的粘贴会贴出 B 的文本。
	// emitText 是平台函数：Windows 走剪贴板 + Ctrl+V；
	// Linux Wayland 走剪贴板 + ydotool Ctrl+V（不可用时试 wtype，
	// 都失败降级为仅剪贴板，返回 errClipboardOnly），X11 走剪贴板 + xdotool。
	pasteMu.Lock()
	defer pasteMu.Unlock()
	if err := emitText(text); err != nil {
		if errors.Is(err, errClipboardOnly) {
			status.set("已复制到剪贴板，请按 Ctrl+V 粘贴")
			return
		}
		status.set(fmt.Sprintf("输出失败: %v", err))
		return
	}
	// 不再每次转写后重启 whisper-server：
	// condition_on_previous_text=false 已防止上下文污染，重启会导致下次请求等待模型重载（10-30s）
	// 仅在检测到异常结果时才需要重启（由 suspiciousResult 触发错误路径）
	// 注意：status 文本会进明文日志文件，这里只记字数，不记语音内容原文。
	status.set(fmt.Sprintf("已粘贴（%d字）", len([]rune(text))))
}

// pasteMu 串行化剪贴板写+粘贴序列，见 doTranscribe。
var pasteMu sync.Mutex

// errClipboardOnly：文本已写进剪贴板，但自动打字不可用
// （Linux Wayland 下 wtype 失败时的降级结果，不是错误；
// 由 paste_linux.go 返回，doTranscribe 据此提示手动 Ctrl+V）。
// 放共享代码里：doTranscribe（共享）要引用它，平台文件按需返回。
var errClipboardOnly = errors.New("clipboard-only")

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
			setHotkeyCode(vkForName(c.Key)) // 配置热重载时同步更新热键
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

// 单实例锁见 mutex_windows.go / mutex_linux.go。
// 自定义快捷键捕获见 capture_windows.go / capture_linux.go。

// ---------- 热键轮询 ----------
// hookKeyDown 由平台热键层设置（无竞态），hotkeyLoop 只需轮询读取。
// toggleMode（Linux Wayland）下走 toggle 语义：上升沿启停录音。
func hotkeyLoop(status *Status) {
	pressed := false
	var pressedAt time.Time
	rec := &Recorder{}
	recording := false
	recWarned := false // 本次录音是否已提示"超长"

	// toggleStop 停止当前录音并按常规规则处理（过短/静音/转写），
	// 供 toggleMode 分支与硬上限分支共用。
	toggleStop := func() {
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

	for {
		if capturing.Load() { // 捕获模式暂停热键
			time.Sleep(25 * time.Millisecond)
			continue
		}
		c := cfg()

		// toggle 模式：hookKeyDown 上升沿切换录音启停（Wayland socket 脉冲驱动）。
		// 注意：toggle 下忽略 HoldMs 与 Mod——组合键由 GNOME 快捷键本身定义。
		if toggleMode {
			hit := hookKeyDown.Load()
			if hit && !pressed {
				pressed = true
				if recording {
					toggleStop()
				} else {
					recording = true
					recordingActive.Store(true)
					if err := rec.Start(); err != nil {
						status.set(fmt.Sprintf("录音失败: %v", err))
						recording = false
						recordingActive.Store(false)
					} else {
						SetActiveRecorder(rec)
						SetOverLimit(false)
						recWarned = false
						status.set("录音中")
					}
				}
			} else if !hit {
				pressed = false
			}
			updateStatusItem(status)
			time.Sleep(25 * time.Millisecond)
			continue
		}

		mv := modVK(c.Mod)
		// hookKeyDown 由平台热键层设置，modifier 由平台 isDown 检测
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
			setHotkeyCode(vkForName(k)) // 同步更新热键
			updateTrayStatus("状态：快捷键已切换为 " + k)
			logf("热键切换为 %s", k)
		},
		onCustom: func() {
			go captureKey()
		},
		onAPI: func() {
			openConfig()
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
	// toggle 子命令（Linux Wayland 模式）：经 socket 通知主进程启停录音。
	// Windows 版不支持，toggleCommandMain 会打印提示并以 1 退出。
	if len(os.Args) > 1 && os.Args[1] == "toggle" {
		os.Exit(toggleCommandMain())
	}
	// 单实例锁：防止多个实例抢占热键 / 残留幽灵图标
	if !acquireMutex() {
		os.Exit(0)
	}
	c := loadCfg()
	cfgPtr.Store(&c)
	setHotkeyCode(vkForName(c.Key)) // 热键层始终拦截此按键
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
	go configHotkeyLoop() // 兜底：Ctrl+Shift+Alt+C 打开配置（托盘菜单失效时也能用；Linux Wayland 下无此兜底）
	go startHotkeyLayer() // 平台热键层（Windows：LL 钩子；Linux X11：抓键；Wayland：socket）
	initOverlay()         // 悬浮窗（录音/转写状态可视；Linux 版映射为托盘 + 通知）
	logf("准备进入托盘循环")
	startTray()
	// 主协程阻塞，保持进程存活（托盘消息循环在 goroutine 中运行）
	<-quitCh
}

// currentKeyName 返回当前配置的热键名（托盘菜单高亮用，平台共享）。
func currentKeyName() string {
	c := cfg()
	if c == nil {
		return "space"
	}
	return c.Key
}
