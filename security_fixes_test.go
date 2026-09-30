//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- P1-3: manifest 文件名路径穿越校验 ----------

func TestSafeManifestFileName(t *testing.T) {
	valid := []string{
		"whisper-server-npu.exe",
		"ggml-large-v3-turbo.bin",
		"a.b.c.exe",
	}
	for _, n := range valid {
		if _, err := safeManifestFileName(n); err != nil {
			t.Errorf("合法文件名 %q 被拒绝: %v", n, err)
		}
	}
	invalid := []string{
		"",
		".",
		"..",
		"../../evil.exe",
		"..\\evil.exe",
		"sub/evil.exe",
		"sub\\evil.exe",
		"/abs/evil.exe",
		"C:\\evil.exe",
		"C:evil.exe",
		"..\\..\\Windows\\evil.exe",
	}
	for _, n := range invalid {
		if _, err := safeManifestFileName(n); err == nil {
			t.Errorf("非法文件名 %q 未被拒绝", n)
		}
	}
}

func TestValidateManifest_RejectsTraversal(t *testing.T) {
	evil := &InstallManifest{Backends: []whisperBackend{
		{Name: "x", ExeName: "../../evil.exe", ModelName: "m.bin"},
	}}
	if err := validateManifest(evil); err == nil {
		t.Error("含路径穿越 ExeName 的清单未被拒绝")
	}
	evilModel := &InstallManifest{Backends: []whisperBackend{
		{Name: "x", ExeName: "ok.exe", ModelName: "..\\evil.bin"},
	}}
	if err := validateManifest(evilModel); err == nil {
		t.Error("含路径穿越 ModelName 的清单未被拒绝")
	}
	good := &InstallManifest{Backends: []whisperBackend{
		{Name: "npu", ExeName: "whisper-server-npu.exe", ModelName: "ggml-large-v3-turbo.bin"},
	}}
	if err := validateManifest(good); err != nil {
		t.Errorf("合法清单被拒绝: %v", err)
	}
	if err := validateManifest(nil); err == nil {
		t.Error("空清单未被拒绝")
	}
}

// ---------- P1-5: suspiciousResult 英文标点白名单 ----------

func TestSuspiciousResult(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"空串", "", false},
		{"短英文", "hello world", false},
		{"占位标记", "please subscribe to my channel for more videos", true},
		{"中文标点", "这是一个很长的中文句子，用来测试标点的存在，完全正常", false},
		// P1-5 核心回归：带英文标点的正常英文长句不再误杀
		{"英文长句带标点", "this is a completely normal english sentence, with commas and a period.", false},
		{"英文长句带问号", "could you please tell me what time the meeting starts tomorrow morning?", false},
		// 仍要能抓到：超长、无任何标点、无中文的疑似幻觉
		{"无标点英文长串", "lalalalalalalalalalalalalalalalalalalalala", true},
	}
	for _, c := range cases {
		if got := suspiciousResult(c.in); got != c.want {
			t.Errorf("%s: suspiciousResult=%v, 期望 %v (输入 %q)", c.name, got, c.want, c.in)
		}
	}
}

// ---------- P1-1: api_key DPAPI 编解码 ----------

func stubDPAPI(t *testing.T) {
	t.Helper()
	oldP, oldU := dpapiProtectFn, dpapiUnprotectFn
	// fake：简单异或混淆，仅验证"编码/解码往返 + 前缀协议"，不依赖真实 DPAPI
	dpapiProtectFn = func(p []byte) ([]byte, error) {
		out := make([]byte, len(p))
		for i, b := range p {
			out[i] = b ^ 0x5A
		}
		return out, nil
	}
	dpapiUnprotectFn = func(p []byte) ([]byte, error) {
		out := make([]byte, len(p))
		for i, b := range p {
			out[i] = b ^ 0x5A
		}
		return out, nil
	}
	t.Cleanup(func() { dpapiProtectFn, dpapiUnprotectFn = oldP, oldU })
}

func TestAPIKeyDPAPIRoundTrip(t *testing.T) {
	stubDPAPI(t)
	const key = "sk-test-secret-12345"
	enc := encodeAPIKeyForSave(key)
	if !strings.HasPrefix(enc, dpapiPrefix) {
		t.Fatalf("编码后缺少 dpapi: 前缀: %q", enc)
	}
	if strings.Contains(enc, key) {
		t.Fatalf("编码后仍含明文密钥")
	}
	if got := decodeAPIKeyForLoad(enc); got != key {
		t.Fatalf("往返解码失败: 得到 %q, 期望 %q", got, key)
	}
	// 幂等：已编码的不再重复加密
	if again := encodeAPIKeyForSave(enc); again != enc {
		t.Fatalf("重复编码改变了值: %q -> %q", enc, again)
	}
}

func TestAPIKeyLegacyPlaintextCompatible(t *testing.T) {
	stubDPAPI(t)
	// 历史明文配置：编码时转为 dpapi 格式，解码时原样透传
	const legacy = "sk-legacy-plain"
	if got := decodeAPIKeyForLoad(legacy); got != legacy {
		t.Fatalf("历史明文未原样透传: %q", got)
	}
	if got := encodeAPIKeyForSave(""); got != "" {
		t.Fatalf("空 key 应原样返回空: %q", got)
	}
	// 损坏的 dpapi 数据：解码失败应清空而非透传 blob
	if got := decodeAPIKeyForLoad(dpapiPrefix + "!!!not-base64!!!"); got != "" {
		t.Fatalf("损坏数据应清空，得到 %q", got)
	}
}

func TestAPIKeyEncodeFallbackOnDPAPIError(t *testing.T) {
	oldP := dpapiProtectFn
	dpapiProtectFn = func([]byte) ([]byte, error) { return nil, errFakeDPAPI }
	t.Cleanup(func() { dpapiProtectFn = oldP })
	// DPAPI 失败时回退明文保存（记 warn），保证程序可用
	if got := encodeAPIKeyForSave("sk-abc"); got != "sk-abc" {
		t.Fatalf("DPAPI 失败时应回退明文，得到 %q", got)
	}
}

type fakeDPAPIError struct{}

func (fakeDPAPIError) Error() string { return "fake dpapi error" }

var errFakeDPAPI error = fakeDPAPIError{}

// ---------- P1-7: multipart 构造 ----------

func TestBuildWavMultipart(t *testing.T) {
	wav := []byte("RIFF....fake-wav-data")
	body, ct, err := buildWavMultipart(wav, [][2]string{{"model", "m"}, {"language", "zh"}})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if !strings.HasPrefix(ct, "multipart/form-data; boundary=") {
		t.Fatalf("Content-Type 异常: %q", ct)
	}
	s := string(body)
	for _, want := range []string{`filename="voice.wav"`, "fake-wav-data", `name="model"`, "m", `name="language"`, "zh"} {
		if !strings.Contains(s, want) {
			t.Errorf("body 缺少 %q", want)
		}
	}
}

// ---------- P1-2: sha256File ----------

func TestSha256File(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.bin")
	content := []byte("hello voice2text")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := sha256File(p)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(content)
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("sha256 不匹配: %s vs %s", sum, hex.EncodeToString(want[:]))
	}
	if _, err := sha256File(filepath.Join(dir, "不存在.bin")); err == nil {
		t.Fatal("不存在的文件应报错")
	}
}
