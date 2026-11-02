//go:build windows

package main

// input_windows.go - 按键状态查询（GetAsyncKeyState）与修饰键映射。
// 原 main.go 中的 isDown/modVK，原样移至此。

func isDown(vk uintptr) bool {
	r, _, _ := getAsync.Call(vk)
	return r&0x8000 != 0
}

func modVK(m string) uintptr {
	switch m {
	case "ctrl":
		return 0x11
	case "alt":
		return 0x12
	case "shift":
		return 0x10
	}
	return 0
}
