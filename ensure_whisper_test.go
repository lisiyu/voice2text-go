package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// resetEnsureState 清空自愈冷却状态，避免测试之间互相影响
func resetEnsureState() {
	wsLifecycleMu.Lock()
	ensureFailed = time.Time{}
	wsLifecycleMu.Unlock()
}

// closedPort 返回一个当前无人监听的端口（健康检查必然失败）
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("无法获取空闲端口: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func serverPort(t *testing.T, u string) int {
	t.Helper()
	p, err := url.Parse(u)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", u, err)
	}
	port, err := strconv.Atoi(p.Port())
	if err != nil {
		t.Fatalf("解析端口失败: %v", err)
	}
	return port
}

// TestEnsureWhisperServer_HealthyFastPath 健康时必须零开销放行：
// 不尝试启动、不记录失败标记、立即返回。
func TestEnsureWhisperServer_HealthyFastPath(t *testing.T) {
	resetEnsureState()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	// exe 为空：一旦走到启动分支就会报错，健康则应在快路径直接返回 nil
	c := &Config{WhisperServerPort: serverPort(t, srv.URL)}

	done := make(chan error, 1)
	go func() { done <- ensureWhisperServer(c) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("健康时应放行，实际: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("健康时 ensure 超过 3s，未走快路径")
	}

	wsLifecycleMu.Lock()
	failedSet := !ensureFailed.IsZero()
	wsLifecycleMu.Unlock()
	if failedSet {
		t.Fatal("健康路径不应记录自愈失败时间")
	}
}

func deadBackend(port int) *Config {
	return &Config{
		WhisperServerExe:  `Z:\definitely\not\exists\whisper-server.exe`,
		WhisperModel:      `Z:\definitely\not\exists\ggml.bin`,
		WhisperServerPort: port,
	}
}

// TestEnsureWhisperServer_FailureSetsCooldown 启动失败后必须进入冷却：
// 60s 内再次调用立即返回冷却错误，而不是每次都阻塞在启动等待上。
func TestEnsureWhisperServer_FailureSetsCooldown(t *testing.T) {
	resetEnsureState()
	c := deadBackend(closedPort(t))

	start := time.Now()
	first := ensureWhisperServer(c)
	firstDur := time.Since(start)
	if first == nil {
		t.Fatal("exe 不存在时应返回错误")
	}
	if !strings.Contains(first.Error(), "自愈失败") {
		t.Fatalf("首次应走自愈并报告失败，实际: %v", first)
	}

	start = time.Now()
	second := ensureWhisperServer(c)
	secondDur := time.Since(start)
	if second == nil {
		t.Fatal("冷却期内应返回错误")
	}
	if !strings.Contains(second.Error(), "后自动重试") {
		t.Fatalf("冷却期内应返回冷却错误，实际: %v", second)
	}
	if secondDur > 5*time.Second {
		t.Fatalf("冷却期内应快速失败，实际耗时 %v", secondDur)
	}
	if firstDur > 60*time.Second {
		t.Fatalf("首次自愈不应超过 60s，实际 %v", firstDur)
	}
	t.Logf("首次自愈耗时 %v，冷却期二次调用耗时 %v", firstDur, secondDur)
}

// TestEnsureWhisperServer_CooldownExpires 冷却过期后应重新尝试自愈。
func TestEnsureWhisperServer_CooldownExpires(t *testing.T) {
	resetEnsureState()
	c := deadBackend(closedPort(t))

	wsLifecycleMu.Lock()
	ensureFailed = time.Now().Add(-ensureCooldown - time.Second)
	wsLifecycleMu.Unlock()

	err := ensureWhisperServer(c)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if strings.Contains(err.Error(), "后自动重试") {
		t.Fatalf("冷却已过期，不应再报冷却错误: %v", err)
	}
	if !strings.Contains(err.Error(), "自愈失败") {
		t.Fatalf("冷却过期后应重新尝试自愈，实际: %v", err)
	}
}

// TestEnsureWhisperServer_SerializesConcurrentCalls 并发转写不得同时拉起多个实例：
// 生命周期锁必须把恢复动作串行化，只有一个真正尝试，其余被冷却拦截。
func TestEnsureWhisperServer_SerializesConcurrentCalls(t *testing.T) {
	resetEnsureState()
	c := deadBackend(closedPort(t))

	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { errs <- ensureWhisperServer(c) }()
	}
	var attemptErr, cooldownErr int
	for i := 0; i < n; i++ {
		err := <-errs
		switch {
		case err == nil:
			t.Fatal("不应成功")
		case strings.Contains(err.Error(), "后自动重试"):
			cooldownErr++
		case strings.Contains(err.Error(), "自愈失败"):
			attemptErr++
		default:
			t.Fatalf("未知错误: %v", err)
		}
	}
	if attemptErr != 1 {
		t.Fatalf("应恰好 1 次真实自愈尝试，实际 %d（冷却拦截 %d）", attemptErr, cooldownErr)
	}
}
