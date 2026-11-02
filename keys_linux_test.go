//go:build linux

package main

import (
	"testing"
)

// TestKeysymRoundTrip 验证所有预设按键名在 vkForName/vkToName 间往返一致，
// 以及 f1-f24 与单字母/数字的换算（纯表逻辑，无需 X 服务器）。
func TestKeysymRoundTrip(t *testing.T) {
	names := []string{
		"space", "f8", "f9",
		"leftshift", "rightshift", "leftctrl", "rightctrl",
		"leftalt", "rightalt", "capslock",
		"home", "end", "insert", "tab", "enter", "delete",
		"left", "right", "up", "down",
		"a", "z", "m", "0", "9",
		"f1", "f12", "f24",
	}
	for _, n := range names {
		code := vkForName(n)
		if code == 0 {
			t.Fatalf("vkForName(%q) = 0", n)
		}
		back := vkToName(code)
		if back != n {
			t.Fatalf("往返不一致: %q -> 0x%X -> %q", n, code, back)
		}
	}
	// 已知的 X11 keysym 值抽查（X11/keysymdef.h）
	if got := vkForName("space"); got != 0x0020 {
		t.Fatalf("space keysym = 0x%X, want 0x0020", got)
	}
	if got := vkForName("f8"); got != 0xFFC5 {
		t.Fatalf("f8 keysym = 0x%X, want 0xFFC5", got)
	}
	if got := vkForName("f9"); got != 0xFFC6 {
		t.Fatalf("f9 keysym = 0x%X, want 0xFFC6", got)
	}
	if got := vkForName("leftctrl"); got != 0xFFE3 {
		t.Fatalf("leftctrl keysym = 0x%X, want 0xFFE3", got)
	}
	if got := vkForName("unknown-key-xyz"); got != 0 {
		t.Fatalf("未知按键应返回 0，实际 0x%X", got)
	}
}
