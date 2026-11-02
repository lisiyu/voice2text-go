//go:build windows

package main

// keys_windows.go - 按键名 ↔ Windows 虚拟键码（VK）映射。
// 原 main.go 中的 vkMap/vkForName/vkToName，原样移至此。
import (
	"fmt"
	"strings"
)

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
