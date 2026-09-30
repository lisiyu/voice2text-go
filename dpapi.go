//go:build windows

package main

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dpapiPrefix 标记 voice2text.json 中 api_key 字段的值是 DPAPI 加密后的
// 数据（base64 编码）。无此前缀的一律视为历史明文，读取时原样兼容。
const dpapiPrefix = "dpapi:"

// dpapiProtect 用 Windows DPAPI (CryptProtectData) 在当前用户上下文加密数据。
// 防御目标：同机其他用户读取、配置文件被整体拷贝/备份到别处后密钥外泄。
// 注意 DPAPI 是用户级保护：同一 Windows 用户下的任意进程仍可解密，
// 因此它不能替代文件权限，只能作为纵深防御的一层。
func dpapiProtect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, fmt.Errorf("dpapi: 拒绝加密空数据")
	}
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var out windows.DataBlob
	// CRYPTPROTECT_UI_FORBIDDEN：禁止弹窗，后台服务场景必需。
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, fmt.Errorf("dpapi 加密失败: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	enc := make([]byte, out.Size)
	copy(enc, unsafe.Slice(out.Data, out.Size))
	return enc, nil
}

// dpapiUnprotect 解密 dpapiProtect 产生的数据。
func dpapiUnprotect(blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, fmt.Errorf("dpapi: 拒绝解密空数据")
	}
	in := windows.DataBlob{Size: uint32(len(blob)), Data: &blob[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, fmt.Errorf("dpapi 解密失败: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	plain := make([]byte, out.Size)
	copy(plain, unsafe.Slice(out.Data, out.Size))
	return plain, nil
}

// 可替换的函数变量，便于测试注入 fake（Windows 上跑单测时用）。
var dpapiProtectFn = dpapiProtect
var dpapiUnprotectFn = dpapiUnprotect

// encodeAPIKeyForSave 把明文 api_key 编码为落盘格式。
// 已是 dpapi: 前缀的直接透传（防止重复加密）；空字符串原样返回。
// DPAPI 加密失败时记 warn 日志并回退明文保存，保证程序可用性。
func encodeAPIKeyForSave(key string) string {
	if key == "" || strings.HasPrefix(key, dpapiPrefix) {
		return key
	}
	enc, err := dpapiProtectFn([]byte(key))
	if err != nil {
		logf("警告: api_key DPAPI 加密失败，将以明文保存: %v", err)
		return key
	}
	return dpapiPrefix + base64.StdEncoding.EncodeToString(enc)
}

// decodeAPIKeyForLoad 把落盘格式还原为明文。
// 无此前缀的视为历史明文配置，原样兼容；解密失败时清空并记 warn
// （残留不可解密的 blob 只会导致上游 401，不如明确提示用户重填）。
func decodeAPIKeyForLoad(key string) string {
	if !strings.HasPrefix(key, dpapiPrefix) {
		return key
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(key, dpapiPrefix))
	if err != nil {
		logf("警告: api_key DPAPI 数据损坏无法解析，已清空，请重新配置: %v", err)
		return ""
	}
	plain, err := dpapiUnprotectFn(raw)
	if err != nil {
		logf("警告: api_key DPAPI 解密失败（可能更换了 Windows 用户），已清空，请重新配置: %v", err)
		return ""
	}
	return string(plain)
}
