//go:build linux

package main

// keys_linux.go - 按键名 ↔ X11 keysym 映射。
// 注意：这里的“码”是 X11 keysym（不是 Windows VK 码，
// 也不是 X keycode）。X11 抓键时再由 xKeysymToKeycode
// 按当前键盘布局换算成 keycode（见 x11_linux.go），
// 因此同一配置在不同布局的机器上都能抓对键。
import (
	"fmt"
	"strings"
)

// X11 keysym（X11/keysymdef.h）：
// space=0x0020；字母/数字与 ASCII 相同；
// F1=0xFFBE 起按序递增；功能键与修饰键见下表。
var vkMap = map[string]uintptr{
	"space":      0x0020,
	"f8":         0xFFC5,
	"f9":         0xFFC6,
	"leftshift":  0xFFE1,
	"rightshift": 0xFFE2,
	"leftctrl":   0xFFE3,
	"rightctrl":  0xFFE4,
	"leftalt":    0xFFE9,
	"rightalt":   0xFFEA,
	"capslock":   0xFFE5,
	"home":       0xFF50,
	"end":        0xFF57,
	"insert":     0xFF63,
	"tab":        0xFF09,
	"enter":      0xFF0D,
	"delete":     0xFFFF,
	"left":       0xFF51,
	"right":      0xFF53,
	"up":         0xFF52,
	"down":       0xFF54,
}

// vkForName 支持预设名 + 任意字母/数字/F1-F24（与 Windows 版同款名字，值是 keysym）。
func vkForName(name string) uintptr {
	name = strings.ToLower(strings.TrimSpace(name))
	if v, ok := vkMap[name]; ok {
		return v
	}
	if len(name) == 1 {
		c := name[0]
		if c >= 'a' && c <= 'z' {
			return uintptr(c) // keysym 与 ASCII 相同
		}
		if c >= '0' && c <= '9' {
			return uintptr(c)
		}
	}
	if len(name) > 1 && name[0] == 'f' {
		var num int
		if _, err := fmt.Sscanf(name[1:], "%d", &num); err == nil && num >= 1 && num <= 24 {
			return uintptr(0xFFBE + num - 1)
		}
	}
	return 0
}

// vkToName 反向映射（keysym → 配置名）
func vkToName(vk uintptr) string {
	for name, v := range vkMap {
		if v == vk {
			return name
		}
	}
	if vk >= 0x61 && vk <= 0x7A {
		return string(rune('a' + vk - 0x61))
	}
	if vk >= 0x30 && vk <= 0x39 {
		return string(rune('0' + vk - 0x30))
	}
	if vk >= 0xFFBE && vk <= 0xFFD5 {
		return fmt.Sprintf("f%d", vk-0xFFBE+1)
	}
	return ""
}

// 修饰键 keysym（捕获模式跳过，与 Windows 版跳过修饰键的逻辑一致）。
var linuxModifierKeysyms = map[uintptr]bool{
	0xFFE1: true, // Shift_L
	0xFFE2: true, // Shift_R
	0xFFE3: true, // Control_L
	0xFFE4: true, // Control_R
	0xFFE9: true, // Alt_L
	0xFFEA: true, // Alt_R
	0xFFE7: true, // Meta_L
	0xFFE8: true, // Meta_R
	0xFFEB: true, // Super_L
	0xFFEC: true, // Super_R
	0xFFE5: true, // Caps_Lock
	0xFF7F: true, // Num_Lock
	0xFF14: true, // Scroll_Lock
}

const linuxKeysymEscape = 0xFF1B
