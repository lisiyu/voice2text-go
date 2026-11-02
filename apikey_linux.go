//go:build linux

package main

// apikey_linux.go - api_key 落盘编解码：Linux 版恒等透传。
// Linux 版只调本地模型，不需要 api_key，因此不引入 keyring 依赖；
// 配置字段保留做兼容。Windows 的 dpapi: 前缀 blob 在 Linux 下
// 无法解密，解码时记警告并清空（与 Windows 版解密失败处理一致）。
import (
	"strings"
)

const dpapiPrefix = "dpapi:"

// encodeAPIKeyForSave：明文原样保存（已是 dpapi: 前缀的直接透传防重复处理）。
func encodeAPIKeyForSave(key string) string {
	return key
}

// decodeAPIKeyForLoad：无前缀的视为明文原样兼容；
// dpapi: 前缀的无法在 Linux 下解密，清空并提示。
func decodeAPIKeyForLoad(key string) string {
	if !strings.HasPrefix(key, dpapiPrefix) {
		return key
	}
	logf("警告: api_key 是 Windows DPAPI 格式，Linux 下无法解密，已清空，请重新配置（如需远程 API）")
	return ""
}
