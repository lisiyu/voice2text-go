package main

// overlay_states.go - 悬浮窗状态常量（平台共享，
// Windows 版画分层窗口，Linux 版映射为托盘状态 + 通知）。
const (
	stRecord = 0
	stTrans  = 1
	stOK     = 2
	stWarn   = 3
)
