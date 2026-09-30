//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// freePort 返回一个当前可用的本机 TCP 端口（用于探测后端，避免固定端口冲突）。
func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 19877
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// ---------- 后端（NPU / GPU / CPU）自动探测与回退 ----------

// whisperBackend 描述一个 whisper 转写后端候选。
// 优先级越小越优先：NPU(最快/最省电) > GPU(Vulkan) > CPU(兜底)。
type whisperBackend struct {
	Name        string // "npu" / "gpu" / "cpu" / "configured"
	Label       string // 展示名
	ExeURL      string // whisper-server.exe 下载地址（留空=仅靠本地已有）
	ModelURL    string // ggml 模型下载地址（留空=仅靠本地已有）
	ExeSHA256   string `json:"exe_sha256,omitempty"`   // exe 的 SHA256（hex），下载后校验；留空=跳过校验并记警告
	ModelSHA256 string `json:"model_sha256,omitempty"` // 模型的 SHA256（hex），下载后校验；留空=跳过校验并记警告
	ExeName     string // 落地后的 exe 文件名（纯文件名，校验见 safeManifestFileName）
	ModelName   string // 落地后的模型文件名（纯文件名，校验见 safeManifestFileName）
	Priority    int    // 越小越优先
	ExePath     string `json:"-"` // 选中后的绝对路径（运行时填充）
	ModelPath   string `json:"-"` // 选中后的绝对路径（运行时填充）
}

// InstallManifest 安装清单：托管在可访问的 URL 或随包内置，描述各后端下载地址。
type InstallManifest struct {
	BaseDir  string           `json:"base_dir"` // 后端/模型落地根目录（install 时可由 --dir 覆盖）
	Backends []whisperBackend `json:"backends"` // 候选后端（按优先级）
}

// defaultManifest 默认后端清单（下载地址需自行托管；留空则该后端仅当本地已有时可用）。
// 以 AMD Ryzen AI (XDNA) NPU 版为首选，GPU(Vulkan) / CPU 版依次回退。
var defaultManifest = &InstallManifest{
	BaseDir: "",
	Backends: []whisperBackend{
		{
			Name: "npu", Label: "NPU (AMD Ryzen AI XDNA)",
			ExeURL: "", ModelURL: "",
			ExeName: "whisper-server-npu.exe", ModelName: "ggml-large-v3-turbo.bin",
			Priority: 0,
		},
		{
			Name: "gpu", Label: "GPU (Vulkan)",
			ExeURL: "", ModelURL: "",
			ExeName: "whisper-server-vulkan.exe", ModelName: "ggml-large-v3-turbo.bin",
			Priority: 1,
		},
		{
			Name: "cpu", Label: "CPU (兜底)",
			ExeURL: "", ModelURL: "",
			ExeName: "whisper-server-cpu.exe", ModelName: "ggml-large-v3-turbo.bin",
			Priority: 2,
		},
	},
}

// prioritizedBackends 按优先级（NPU>GPU>CPU）排序返回后端列表。
func prioritizedBackends(m *InstallManifest) []whisperBackend {
	bs := append([]whisperBackend{}, m.Backends...)
	for i := 0; i < len(bs); i++ {
		for j := i + 1; j < len(bs); j++ {
			if bs[j].Priority < bs[i].Priority {
				bs[i], bs[j] = bs[j], bs[i]
			}
		}
	}
	return bs
}

// backendDir 返回后端落地的 bin 目录（优先 --dir，其次 exe 同目录下的 bin/）。
func backendDir(dir string) string {
	if dir != "" {
		return filepath.Join(dir, "bin")
	}
	exe, err := os.Executable()
	if err != nil {
		return "bin"
	}
	return filepath.Join(filepath.Dir(exe), "bin")
}

// resolveExe / resolveModel 返回某后端 exe / 模型在本机的绝对路径。
func resolveExe(b whisperBackend, dir string) string {
	return filepath.Join(backendDir(dir), b.ExeName)
}
func resolveModel(b whisperBackend, dir string) string {
	return filepath.Join(backendDir(dir), b.ModelName)
}

// fileExists 判断文件是否存在且非空。
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Size() > 0
}

// downloadFile 下载 url 到 dst，带进度输出（仅安装阶段使用）。
// wantSHA256 非空时对下载文件做 SHA256 校验（hex，不区分大小写）；
// 校验失败删除临时文件并报错。wantSHA256 为空时跳过校验并记警告日志。
func downloadFile(url, dst, wantSHA256 string) error {
	if url == "" {
		return fmt.Errorf("未配置下载地址")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".part"
	// 任何失败路径都不留 .part 残留；成功 rename 后 tmp 已不存在，Remove 无害。
	defer os.Remove(tmp)
	client := &http.Client{Timeout: 10 * time.Minute} // 大模型文件下载允许 10 分钟
	resp, err := client.Get(url)                      //nolint:gosec // 安装器受信任清单内的地址
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("下载 %s 失败: HTTP %d", url, resp.StatusCode)
	}
	total := resp.ContentLength
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<16)
	var written int64
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			written += int64(n)
			if time.Since(last) > 500*time.Millisecond && total > 0 {
				fmt.Fprintf(os.Stderr, "  下载 %s ... %.1f%%\r", filepath.Base(dst), float64(written)/float64(total)*100)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	out.Close()
	if wantSHA256 != "" {
		sum, err := sha256File(tmp)
		if err != nil {
			return fmt.Errorf("计算 SHA256 失败: %w", err)
		}
		if !strings.EqualFold(sum, wantSHA256) {
			return fmt.Errorf("SHA256 校验失败（疑似下载损坏或被篡改）: 期望 %s，实际 %s", wantSHA256, sum)
		}
		fmt.Fprintf(os.Stderr, "  SHA256 校验通过 %s\n", filepath.Base(dst))
	} else {
		fmt.Fprintf(os.Stderr, "  [警告] %s 未提供 SHA256 校验和，跳过完整性校验\n", filepath.Base(dst))
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  下载完成 %s (%.1f MB)\n", filepath.Base(dst), float64(written)/1024/1024)
	return nil
}

// sha256File 计算文件的 SHA256 hex。
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// probeWhisperServer 临时拉起 whisper-server 验证该后端在本机是否可用（NPU/GPU 驱动是否就绪）。
// 成功返回 true（并会关闭临时进程），失败返回 false。
func probeWhisperServer(exe, model string, port int) bool {
	// 动态线程数：CPU核心数，上限16
	nThreads := runtime.NumCPU()
	if nThreads > 16 {
		nThreads = 16
	}
	cmd := exec.Command(exe, "--host", "127.0.0.1", "--port", fmt.Sprintf("%d", port),
		"-m", model, "-l", "zh", "-t", fmt.Sprintf("%d", nThreads))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return false
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(60 * time.Second) // NPU 载入大模型可能需要更长时间，增加超时阈值
	for time.Now().Before(deadline) {
		if c2, err := net.DialTimeout("tcp", addr, 800*time.Millisecond); err == nil {
			_ = c2.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// selectBackend 按 NPU>GPU>CPU 顺序探测，返回首个在本机可运行的后端。
// 探测策略：本机已有 exe/模型则直接试用；若清单提供下载地址则先下载再试。
// 这正满足"不支持 NPU 就自动回退到 GPU 或 CPU"的需求（运行时实测而非静态判断）。
func selectBackend(m *InstallManifest, dir string) (whisperBackend, error) {
	// 0) 已配置且本地存在的后端优先（用户可能已具备可用环境，如本机 NPU）
	cc := loadCfg() // 直接用 loadCfg（安装阶段可能早于 cfgPtr 初始化）
	if cc.WhisperServerExe != "" && fileExists(cc.WhisperServerExe) &&
		cc.WhisperModel != "" && fileExists(cc.WhisperModel) {
		fmt.Fprintf(os.Stderr, "[探测] 试启动已配置后端 %s ...\n", cc.WhisperServerExe)
		if probeWhisperServer(cc.WhisperServerExe, cc.WhisperModel, freePort()) {
			fmt.Fprintf(os.Stderr, "[探测] ✅ 选中已配置后端\n")
			return whisperBackend{
				Name: "configured", Label: "已配置后端",
				ExePath: cc.WhisperServerExe, ModelPath: cc.WhisperModel,
			}, nil
		}
		fmt.Fprintf(os.Stderr, "[探测] 已配置后端不可用，回退清单候选\n")
	}

	for _, b := range prioritizedBackends(m) {
		exe := resolveExe(b, dir)
		model := resolveModel(b, dir)
		// 确保 exe 存在
		if !fileExists(exe) {
			if b.ExeURL != "" {
				fmt.Fprintf(os.Stderr, "[探测] 下载 %s 后端 exe...\n", b.Label)
				if err := downloadFile(b.ExeURL, exe, b.ExeSHA256); err != nil {
					fmt.Fprintf(os.Stderr, "[探测] %s exe 获取失败: %v，跳过\n", b.Label, err)
					continue
				}
			} else {
				continue // 既无本地也无下载地址，跳过
			}
		}
		// 确保模型存在
		if !fileExists(model) {
			if b.ModelURL != "" {
				fmt.Fprintf(os.Stderr, "[探测] 下载 %s 模型...\n", b.Label)
				if err := downloadFile(b.ModelURL, model, b.ModelSHA256); err != nil {
					fmt.Fprintf(os.Stderr, "[探测] %s 模型获取失败: %v，跳过\n", b.Label, err)
					continue
				}
			} else {
				continue
			}
		}
		fmt.Fprintf(os.Stderr, "[探测] 试启动 %s (port 临时)...\n", b.Label)
		if probeWhisperServer(exe, model, freePort()) {
			fmt.Fprintf(os.Stderr, "[探测] ✅ 选中后端: %s\n", b.Label)
			b.ExePath = exe
			b.ModelPath = model
			return b, nil
		}
		fmt.Fprintf(os.Stderr, "[探测] %s 在本机不可用（驱动/依赖缺失），回退下一优先级\n", b.Label)
	}
	return whisperBackend{}, fmt.Errorf("未找到任何可用后端（NPU/GPU/CPU 均不可用）")
}

// pickLocalBackend 启动阶段轻量选后端：仅复用本机【已存在】的 exe+模型，
// 不下载、不临时拉起进程（避免重复加载大模型）。找不到返回 ok=false。
// 真正的“运行时探测 + 下载”只在 install 子命令的 selectBackend 里做。
func pickLocalBackend(m *InstallManifest) (whisperBackend, bool) {
	cc := loadCfg()
	if cc.WhisperServerExe != "" && fileExists(cc.WhisperServerExe) &&
		cc.WhisperModel != "" && fileExists(cc.WhisperModel) {
		return whisperBackend{Name: "configured", Label: "已配置后端",
			ExePath: cc.WhisperServerExe, ModelPath: cc.WhisperModel}, true
	}
	for _, b := range prioritizedBackends(m) {
		exe := resolveExe(b, "")
		model := resolveModel(b, "")
		if fileExists(exe) && fileExists(model) {
			b.ExePath = exe
			b.ModelPath = model
			return b, true
		}
	}
	return whisperBackend{}, false
}

// safeManifestFileName 校验清单中的文件名：必须是纯文件名，不允许任何路径成分。
// ExeName/ModelName 来自远端 manifest（用户可指定 --manifest），未经校验直接
// filepath.Join(backendDir, name) 会导致路径穿越写出 bin 目录。
func safeManifestFileName(name string) (string, error) {
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("非法文件名: %q", name)
	}
	if filepath.Base(name) != name {
		return "", fmt.Errorf("非法文件名（含路径成分）: %q", name)
	}
	if len(name) >= 2 && name[1] == ':' {
		return "", fmt.Errorf("非法文件名（含盘符）: %q", name)
	}
	return name, nil
}

// validateManifest 校验清单中所有文件名字段，任一非法即拒绝整个清单。
func validateManifest(m *InstallManifest) error {
	if m == nil {
		return fmt.Errorf("空清单")
	}
	for i, b := range m.Backends {
		if _, err := safeManifestFileName(b.ExeName); err != nil {
			return fmt.Errorf("后端[%d] ExeName %w", i, err)
		}
		if _, err := safeManifestFileName(b.ModelName); err != nil {
			return fmt.Errorf("后端[%d] ModelName %w", i, err)
		}
	}
	return nil
}

// loadManifest 加载后端清单：优先 --manifest URL，否则用内置默认清单。
func loadManifest(manifestURL string) *InstallManifest {
	if manifestURL != "" {
		if b, err := fetchManifest(manifestURL); err == nil {
			if verr := validateManifest(b); verr == nil {
				return b
			} else {
				fmt.Fprintf(os.Stderr, "[清单] 远程清单文件名校验失败（疑似路径穿越），使用内置默认清单: %v\n", verr)
			}
		} else {
			fmt.Fprintf(os.Stderr, "[清单] 远程清单加载失败，使用内置默认清单\n")
		}
	}
	return defaultManifest
}

func fetchManifest(url string) (*InstallManifest, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var m InstallManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		io.Copy(io.Discard, resp.Body)
		return nil, err
	}
	return &m, nil
}

// runInstall 安装子命令：探测环境→选后端→下载依赖→写配置。
func runInstall(dir, manifestURL string) error {
	m := loadManifest(manifestURL)
	fmt.Fprintf(os.Stderr, "== voice2text 安装 ==\n")
	fmt.Fprintf(os.Stderr, "[环境] 探测 NPU/GPU/CPU 后端...\n")
	b, err := selectBackend(m, dir)
	if err != nil {
		return err
	}
	exe := b.ExePath
	if exe == "" {
		exe = resolveExe(b, dir)
	}
	model := b.ModelPath
	if model == "" {
		model = resolveModel(b, dir)
	}
	// 写配置：让常驻 whisper-server 直接用选中的后端
	c := defaultCfg()
	// 保留用户原有 api/model 兜底
	if old, err := os.ReadFile(cfgPath()); err == nil {
		_ = json.Unmarshal(old, &c)
	}
	c.WhisperServerExe = exe
	c.WhisperModel = model
	c.WhisperServerURL = fmt.Sprintf("http://127.0.0.1:%d/inference", c.WhisperServerPort)
	c.AutoBackend = true // 下次启动仍按环境自动回退
	saveCfg(c)
	fmt.Fprintf(os.Stderr, "[完成] 已选用 %s\n  exe  : %s\n  model: %s\n  配置已写入 %s\n", b.Label, exe, model, cfgPath())
	fmt.Fprintf(os.Stderr, "[提示] 直接运行 voice2text.exe 即可（常驻转写服务会随启动自动拉起）。\n")
	return nil
}
