//go:build linux

package main

import (
	"path/filepath"
	"testing"
	"time"
)

// TestEvdevCodeForName 验证配置按键名 → evdev 码（预设名/字母/数字/f1-f24，
// 与 linux/input-event-codes.h 对照），未知名字返回 false。
func TestEvdevCodeForName(t *testing.T) {
	want := map[string]uint16{
		"space": 57, "f8": 66, "f9": 67,
		"leftshift": 42, "rightshift": 54,
		"leftctrl": 29, "rightctrl": 97,
		"leftalt": 56, "rightalt": 100,
		"capslock": 58, "home": 102, "end": 107, "insert": 110,
		"tab": 15, "enter": 28, "delete": 111,
		"left": 105, "right": 106, "up": 103, "down": 108,
		"a": 30, "z": 44, "m": 50, "q": 16,
		"1": 2, "9": 10, "0": 11,
		"f1": 59, "f10": 68, "f11": 87, "f12": 88, "f13": 183, "f24": 194,
	}
	for name, code := range want {
		got, ok := evdevCodeForName(name)
		if !ok || got != code {
			t.Fatalf("evdevCodeForName(%q) = %d, %v; want %d, true", name, got, ok, code)
		}
	}
	// 大小写与空白容忍（与 vkForName 一致的归一化）
	if got, ok := evdevCodeForName(" F8 "); !ok || got != 66 {
		t.Fatalf("evdevCodeForName(\" F8 \") = %d, %v; want 66, true", got, ok)
	}
	for _, bad := range []string{"", "unknown-key-xyz", "f0", "f25", "f1a", "space2"} {
		if got, ok := evdevCodeForName(bad); ok {
			t.Fatalf("evdevCodeForName(%q) = %d, true; want false", bad, got)
		}
	}
}

// TestEvdevHotkeyStateNoMod：无修饰键时按下/重复/松开的状态变化。
func TestEvdevHotkeyStateNoMod(t *testing.T) {
	st := &evdevHotkeyState{mainCode: 66} // f8
	if st.handle(66, 1) != true {
		t.Fatal("按下主键后 hit 应为 true")
	}
	if st.handle(66, 2) != true {
		t.Fatal("自动重复（value=2）不应改变状态")
	}
	if st.handle(30, 1) != true {
		t.Fatal("无关按键不应改变 hit（主键仍按下）")
	}
	if st.handle(30, 0) != true {
		t.Fatal("无关按键松开不应改变 hit")
	}
	if st.handle(66, 0) != false {
		t.Fatal("主键松开后 hit 应为 false")
	}
}

// TestEvdevHotkeyStateWithMod：带修饰键时主键与修饰键缺一不可。
func TestEvdevHotkeyStateWithMod(t *testing.T) {
	st := &evdevHotkeyState{mainCode: 66, modBit: evdevModCtrl}
	if st.handle(66, 1) != false {
		t.Fatal("未按 ctrl 时只按主键 hit 应为 false")
	}
	if st.handle(29, 1) != true {
		t.Fatal("补按左 ctrl 后 hit 应为 true")
	}
	if st.handle(29, 0) != false {
		t.Fatal("主键仍按住但 ctrl 松开后 hit 应为 false")
	}
	if st.handle(97, 1) != true {
		t.Fatal("右 ctrl 同样满足 ctrl 修饰（左右不区分）")
	}
	if st.handle(66, 0) != false {
		t.Fatal("主键松开后 hit 应为 false")
	}
}

// TestEvdevHotkeyStateMainIsMod：主键本身就是修饰键名（如 leftctrl）时也能成立。
func TestEvdevHotkeyStateMainIsMod(t *testing.T) {
	st := &evdevHotkeyState{mainCode: 29}
	if st.handle(29, 1) != true {
		t.Fatal("主键 leftctrl 按下后 hit 应为 true")
	}
	if st.handle(29, 0) != false {
		t.Fatal("主键 leftctrl 松开后 hit 应为 false")
	}
}

// TestHasKeyboardKeys：EV_KEY 位图过滤（esc/enter/a/space/f1 任一命中即键盘，
// 只有 BTN_* 位的鼠标/触摸板不命中，短位图不 panic）。
func TestHasKeyboardKeys(t *testing.T) {
	setBit := func(bits []byte, code uint16) {
		bits[code/8] |= 1 << (code % 8)
	}
	keyboard := make([]byte, 96)
	setBit(keyboard, 28) // enter
	if !hasKeyboardKeys(keyboard) {
		t.Fatal("含 enter 位的设备应判定为键盘")
	}
	mouse := make([]byte, 96)
	setBit(mouse, 272) // BTN_LEFT
	setBit(mouse, 273) // BTN_RIGHT
	if hasKeyboardKeys(mouse) {
		t.Fatal("只有 BTN 位的设备不应判定为键盘")
	}
	if hasKeyboardKeys(make([]byte, 96)) {
		t.Fatal("空位图不应判定为键盘")
	}
	if hasKeyboardKeys(make([]byte, 1)) {
		t.Fatal("短位图不应判定为键盘（且不应 panic）")
	}
}

// TestStartEvdevHotkeyNoDevice：无输入设备时启动函数必须返回 false
// （调用方据此回落 socket toggle），且不得 panic。
// 本机有输入设备时跳过（避免在真机上误判）。
func TestStartEvdevHotkeyNoDevice(t *testing.T) {
	if paths, _ := filepath.Glob("/dev/input/event*"); len(paths) > 0 {
		t.Skip("本机有输入设备，跳过无设备回落测试")
	}
	if startEvdevHotkey() {
		t.Fatal("无设备时 startEvdevHotkey 应返回 false")
	}
}

// TestEvdevAggregatorDrivesHookKeyDown：聚合器把合成事件翻译成 hookKeyDown，
// 且配置切换（f8→f9）后旧键失效、新键生效。
func TestEvdevAggregatorDrivesHookKeyDown(t *testing.T) {
	oldCfg := cfgPtr.Load()
	oldDown := hookKeyDown.Load()
	defer func() {
		if oldCfg != nil {
			cfgPtr.Store(oldCfg)
		}
		hookKeyDown.Store(oldDown)
	}()
	cfgPtr.Store(&Config{Key: "f8", Mod: "", HoldMs: 300})
	hookKeyDown.Store(false)

	ch := make(chan evdevEvent, 16)
	go evdevAggregator(ch)
	defer close(ch)

	waitDown := func(want bool) {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if hookKeyDown.Load() == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("hookKeyDown 在 1s 内未变为 %v", want)
	}

	ch <- evdevEvent{code: 66, value: 1} // f8 按下
	waitDown(true)
	ch <- evdevEvent{code: 66, value: 0} // f8 松开
	waitDown(false)

	cfgPtr.Store(&Config{Key: "f9", Mod: "", HoldMs: 300})
	ch <- evdevEvent{code: 66, value: 1} // 旧键 f8：应失效
	time.Sleep(50 * time.Millisecond)
	if hookKeyDown.Load() {
		t.Fatal("配置切到 f9 后，旧键 f8 按下不应驱动 hookKeyDown")
	}
	ch <- evdevEvent{code: 67, value: 1} // 新键 f9 按下
	waitDown(true)
	ch <- evdevEvent{code: 67, value: 0}
	waitDown(false)
}

// TestEvdevModCodes：修饰键名 → 左右码 + 状态位。
func TestEvdevModCodes(t *testing.T) {
	codes, bit := evdevModCodes("ctrl")
	if bit != evdevModCtrl || len(codes) != 2 || codes[0] != 29 || codes[1] != 97 {
		t.Fatalf("ctrl 映射错误：codes=%v bit=%d", codes, bit)
	}
	if _, bit := evdevModCodes(""); bit != 0 {
		t.Fatalf("空修饰键 bit 应为 0，实际 %d", bit)
	}
	if _, bit := evdevModCodes("meta"); bit != 0 {
		t.Fatalf("未知修饰键 bit 应为 0，实际 %d", bit)
	}
}
