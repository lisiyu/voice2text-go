//go:build windows

package main

import _ "embed"

//go:embed icon_mic_128.png
var micIconPNG []byte

//go:embed icon_check_128.png
var checkIconPNG []byte

//go:embed icon_warn_128.png
var warnIconPNG []byte

// genMicPNG 返回 AI 生成的精致麦克风图标（128x128）
func genMicPNG() []byte { return micIconPNG }

// genCheckPNG 返回 AI 生成的精致对勾图标（128x128）
func genCheckPNG() []byte { return checkIconPNG }

// genWarnPNG 返回 AI 生成的精致感叹号图标（128x128）
func genWarnPNG() []byte { return warnIconPNG }
