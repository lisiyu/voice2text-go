//go:build linux

package main

// audio_linux.go - 录音：PipeWire 的 pw-record 子进程，
// 从 stdout 流式读原始 s16le 16kHz 单声道 PCM。
// --raw 表示不写 WAV 头；若某版本 pw-record 仍输出 WAV 头，
// readLoop 会检测并跳过 44 字节标准头。
import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// recorderSys 是 Recorder 的 Linux 专属状态。
type recorderSys struct {
	cmd *exec.Cmd
}

// Start 拉起 pw-record 并开始录音
func (r *Recorder) Start() error {
	cmd := exec.Command("pw-record", "--format=s16", "--rate=16000", "--channels=1", "--raw", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pw-record StdoutPipe 失败: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 pw-record 失败（是否未安装 pipewire？）: %w", err)
	}
	r.sys.cmd = cmd
	r.running.Store(true)
	r.recStart = time.Now()
	go r.readLoop(stdout, &stderr)
	return nil
}

// readLoop 从 pw-record stdout 读 PCM 分块，拷入 data 并算 RMS。
// 首块若以 "RIFF" 开头则视为 WAV 头，跳过 44 字节。
func (r *Recorder) readLoop(stdout io.Reader, stderr *bytes.Buffer) {
	buf := make([]byte, bufBytes)
	headerChecked := false
	var pending []byte // WAV 头跨块到达时的暂存
	for r.running.Load() {
		n, err := stdout.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if !headerChecked {
				headerChecked = true
				// 头可能分两次到达：先攒够 4 字节再判断
				if len(pending)+len(chunk) < 4 {
					pending = append(pending, chunk...)
					continue
				}
				full := append(pending, chunk...)
				pending = nil
				if len(full) >= 4 && string(full[:4]) == "RIFF" {
					// WAV 头：等够 44 字节再跳过（头本身也可能跨块）
					if len(full) < 44 {
						pending = full
						continue
					}
					chunk = full[44:]
				} else {
					chunk = full
				}
			} else if len(pending) > 0 {
				// 上一块攒了个不完整的 WAV 头候选：已确认不是 RIFF 时在此合并
				chunk = append(pending, chunk...)
				pending = nil
			}
			if len(chunk) > 0 {
				r.mu.Lock()
				r.data = append(r.data, chunk...)
				r.mu.Unlock()
				const subN = 4
				step := len(chunk) / subN
				if step < 4 {
					step = 4
				}
				for s := 0; s+step <= len(chunk); s += step {
					r.pushLevel(rms(chunk[s : s+step]))
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				logf("pw-record 读取错误: %v stderr=%s", err, stderr.String())
			}
			break
		}
	}
}

// Stop 停止 pw-record 并返回 16k/16bit/mono PCM 数据。
// 先发 SIGINT 让 pw-record 优雅收尾（PipeWire 工具收到 SIGINT 会正常退出），
// 超时未退再 SIGKILL；残余 stdout 由 readLoop 自然读完（Stop 前先停 readLoop 标记）。
func (r *Recorder) Stop() ([]byte, error) {
	r.running.Store(false)
	cmd := r.sys.cmd
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGINT)
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	// 给 readLoop 一点时间把管道残余读完
	time.Sleep(100 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data, nil
}
