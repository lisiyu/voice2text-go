//go:build linux

package main

// evdev_linux.go - Wayland 按住热键：被动读取 evdev 键盘设备。
//   背景：Wayland 下普通应用拿不到全局热键（见 hook_linux.go），
//     GNOME 快捷键又只能表达 toggle（按一下/再按一下），
//     恢复不了 Windows 版“按住说话、松开停止”的语义。
//     直接读 /dev/input/event* 不依赖 X11 也不依赖合成器，
//     在 Wayland/X11/裸控制台下都一样工作；
//     读设备节点需要 input 组成员身份（见 install.sh 的 3b 节）。
//   设计（只被动读，绝不 EVIOCGRAB）：
//     - 启动时枚举 /dev/input/event*，用 EVIOCGBIT(EV_KEY) 过滤出键盘类设备，
//       每设备一条读 goroutine，事件汇总到一条 channel 由聚合 goroutine 处理；
//       读不到设备或无权限时返回 false，调用方静默回落 socket toggle。
//     - 聚合 goroutine 维护修饰键状态 + 主键按下状态，
//       按当前配置（cfg()，热键切换/配置重载自动跟随）算出 hit，
//       驱动现有的 hookKeyDown；hotkeyLoop 的按住语义（HoldMs、松开停止）
//       不用改。修饰键状态同时经 evdevModMask 暴露给 input_linux.go 的 isDown，
//       这样带 mod 的组合键在 Wayland 下也能成立。
//   局限（写明，不谎报）：
//     - 被动读拦不住按键：事件在送本程序的同时仍会送焦点窗口。
//       因此按住模式只对 F8 这类不打字的键干净；
//       空格等打字键按住时会把字符灌进文档，
//       想要干净的长按空格必须走独占抓取 + uinput 转发全键盘的拦截器
//       （Wisper 的 handy-keys blocking 模式就是这么做的，
//       需要 /dev/uinput 写权限，本轮不做），
//       所以空格继续走 socket toggle，不要配成按住键。
//     - 设备热插拔不跟进：启动后新插的键盘要重启程序才读到；
//       设备中途拔掉只记日志，不触发回落（回落只在启动时判定）。
import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	evdevTypeKey = 0x01 // EV_KEY

	// EVIOCGBIT(EV_KEY, len) = _IOC(_IOC_READ, 'E', 0x20+EV_KEY, len)，
	// 即 (2<<30) | ('E'<<8) | 0x21 | (len<<16)。
	evdevKeyBitsLen = 96 // KEY_MAX 0x2ff 共 768 bit，96 字节
)

var evdevIOCGBitKey = uintptr(0x80000000 | 0x4500 | 0x21 | (evdevKeyBitsLen << 16))

// evdevActive：evdev 按住模式是否已接管（input_linux.go 的 isDown 据此取修饰键状态）。
var evdevActive atomic.Bool

// evdevModMask：当前按下的修饰键位图（ctrl=1，alt=2，shift=4），聚合 goroutine 维护。
var evdevModMask atomic.Uint32

const (
	evdevModCtrl  uint32 = 1
	evdevModAlt   uint32 = 2
	evdevModShift uint32 = 4
)

// evdevKeyMap：配置按键名 → evdev 码（linux/input-event-codes.h）。
// 名字与 keys_linux.go 的 vkMap 同款（space/f8/f9/左右修饰/capslock/home/end/
// insert/tab/enter/delete/方向键），值是 evdev 码不是 keysym。
var evdevKeyMap = map[string]uint16{
	"space":      57,
	"f8":         66,
	"f9":         67,
	"leftshift":  42,
	"rightshift": 54,
	"leftctrl":   29,
	"rightctrl":  97,
	"leftalt":    56,
	"rightalt":   100,
	"capslock":   58,
	"home":       102,
	"end":        107,
	"insert":     110,
	"tab":        15,
	"enter":      28,
	"delete":     111,
	"left":       105,
	"right":      106,
	"up":         103,
	"down":       108,
}

// evdevLetterCodes：a-z 的 evdev 码（顺序 a..z）。
var evdevLetterCodes = [26]uint16{
	30, 48, 46, 32, 18, 33, 34, 35, 23, 36, 37, 38,
	50, 49, 24, 25, 16, 19, 31, 20, 22, 47, 17, 45, 21, 44,
}

// evdevCodeForName：配置按键名 → evdev 码（预设名 + 单字母/数字 + f1-f24，
// 与 vkForName 同款名字）。未知名字返回 ok=false。
func evdevCodeForName(name string) (uint16, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if c, ok := evdevKeyMap[name]; ok {
		return c, true
	}
	if len(name) == 1 {
		c := name[0]
		if c >= 'a' && c <= 'z' {
			return evdevLetterCodes[c-'a'], true
		}
		if c >= '1' && c <= '9' {
			return uint16(1 + c - '0'), true // '1'→2 … '9'→10
		}
		if c == '0' {
			return 11, true
		}
	}
	if len(name) > 1 && name[0] == 'f' {
		if n, ok := parseFNum(name[1:]); ok && n >= 1 && n <= 24 {
			return evdevFCode(n), true
		}
	}
	return 0, false
}

// evdevFCode：f1-f24 的 evdev 码（f1=59…f10=68，f11=87，f12=88，f13=183…f24=194）。
func evdevFCode(n int) uint16 {
	switch {
	case n >= 1 && n <= 10:
		return uint16(58 + n)
	case n == 11:
		return 87
	case n == 12:
		return 88
	default: // 13-24
		return uint16(170 + n)
	}
}

// parseFNum：解析 f 后面的数字（只接受纯十进制）。
func parseFNum(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// evdevModCodes：配置修饰键名 → 该修饰键的左右 evdev 码 + 状态位。
func evdevModCodes(mod string) (codes []uint16, bit uint32) {
	switch mod {
	case "ctrl":
		return []uint16{29, 97}, evdevModCtrl
	case "alt":
		return []uint16{56, 100}, evdevModAlt
	case "shift":
		return []uint16{42, 54}, evdevModShift
	}
	return nil, 0
}

// evdevModCodeBit：单个 evdev 码属于哪个修饰键位（0 表示不是修饰键）。
func evdevModCodeBit(code uint16) uint32 {
	switch code {
	case 29, 97:
		return evdevModCtrl
	case 56, 100:
		return evdevModAlt
	case 42, 54:
		return evdevModShift
	}
	return 0
}

// evdevIsModDown：input_linux.go 的 isDown 在 evdev 模式下查修饰键状态的入口。
// vk 是 keysym（modVK 的返回值：ctrl=0xFFE3，alt=0xFFE9，shift=0xFFE1）。
func evdevIsModDown(vk uintptr) bool {
	var bit uint32
	switch vk {
	case 0xFFE3:
		bit = evdevModCtrl
	case 0xFFE9:
		bit = evdevModAlt
	case 0xFFE1:
		bit = evdevModShift
	default:
		return false
	}
	return evdevModMask.Load()&bit != 0
}

// hasKeyboardKeys：EV_KEY 位图里是否含典型键盘按键（esc/enter/a/space/f1 任一）。
// 鼠标类设备只有 BTN_* 位（0x110 起），不会命中；触摸板同理。
func hasKeyboardKeys(bits []byte) bool {
	for _, code := range []uint16{1, 28, 30, 57, 59} {
		if int(code/8) < len(bits) && bits[code/8]&(1<<(code%8)) != 0 {
			return true
		}
	}
	return false
}

// evdevIsKeyboard：对已打开的设备做 EVIOCGBIT(EV_KEY) 过滤。
// ioctl 失败（非常见）时按非键盘处理，不冒险读事件。
func evdevIsKeyboard(f *os.File) bool {
	bits := make([]byte, evdevKeyBitsLen)
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), evdevIOCGBitKey,
		uintptr(unsafe.Pointer(&bits[0])))
	if errno != 0 {
		return false
	}
	return hasKeyboardKeys(bits)
}

type evdevEvent struct {
	code  uint16
	value int32 // 0=松开，1=按下，2=自动重复
}

// evdevHotkeyState：聚合 goroutine 的单线程状态机（无锁，只在聚合 goroutine 里碰）。
type evdevHotkeyState struct {
	mainCode uint16
	mainDown bool
	modMask  uint32
	modBit   uint32 // 本次配置要求的修饰键位，0 表示不需要
}

func (s *evdevHotkeyState) hit() bool {
	return s.mainDown && (s.modBit == 0 || s.modMask&s.modBit != 0)
}

// handle：输入一个 EV_KEY 事件，返回当前 hit 状态。
// 主键先于修饰键判断：主键本身就是修饰键名（如 leftctrl）时也能成立。
// value==2（自动重复）不改变状态。
func (s *evdevHotkeyState) handle(code uint16, value int32) bool {
	if value == 2 {
		return s.hit()
	}
	down := value == 1
	if code == s.mainCode {
		s.mainDown = down
	}
	if bit := evdevModCodeBit(code); bit != 0 {
		if down {
			s.modMask |= bit
		} else {
			s.modMask &^= bit
		}
	}
	return s.hit()
}

// startEvdevHotkey：启动 evdev 按住热键。至少打开一个键盘设备返回 true；
// 一个都打不开（无权限/无设备）时返回 false，调用方回落 socket toggle。
// 注意：只被动读，绝不 EVIOCGRAB——读到的事件仍会正常送焦点窗口。
func startEvdevHotkey() bool {
	paths, _ := filepath.Glob("/dev/input/event*")
	evCh := make(chan evdevEvent, 128)
	opened := 0
	for _, p := range paths {
		f, err := os.OpenFile(p, os.O_RDONLY, 0)
		if err != nil {
			continue
		}
		if !evdevIsKeyboard(f) {
			f.Close()
			continue
		}
		opened++
		go evdevReader(f, evCh)
	}
	if opened == 0 {
		logf("evdev 热键：未找到可读键盘设备（是否未加入 input 组？见 install.sh 3b 节），回落 socket toggle")
		return false
	}
	go evdevAggregator(evCh)
	evdevActive.Store(true)
	logf("evdev 热键：已接管 %d 个键盘设备（被动读取，不抓取；字符键按住仍会灌注，见 evdev_linux.go 局限）", opened)
	return true
}

// evdevReader：单设备阻塞读事件循环；只转发 EV_KEY 事件。
// 设备拔掉/读错时退出（聚合 goroutine 不因此回落，见文件头局限）。
func evdevReader(f *os.File, evCh chan<- evdevEvent) {
	defer f.Close()
	var buf [24]byte // struct input_event（64 位）：timeval 16 + type 2 + code 2 + value 4
	for {
		if _, err := io.ReadFull(f, buf[:]); err != nil {
			logf("evdev 热键：设备读取结束: %v", err)
			return
		}
		if binary.LittleEndian.Uint16(buf[16:18]) != evdevTypeKey {
			continue
		}
		ev := evdevEvent{
			code:  binary.LittleEndian.Uint16(buf[18:20]),
			value: int32(binary.LittleEndian.Uint32(buf[20:24])),
		}
		select {
		case evCh <- ev:
		default:
			// 通道满时丢弃本次事件（不阻塞读循环；按键状态由后续事件纠正）。
		}
	}
}

// evdevAggregator：单 goroutine 聚合所有键盘设备的事件，驱动 hookKeyDown。
// 每次事件都重读 cfg()：托盘切换热键/配置重载自动跟随；
// 配置变化时复位状态，避免旧键 stuck。
func evdevAggregator(evCh <-chan evdevEvent) {
	st := &evdevHotkeyState{}
	lastKey, lastMod := "", ""
	for ev := range evCh {
		c := cfg()
		if c == nil {
			continue
		}
		code, ok := evdevCodeForName(c.Key)
		if !ok {
			if hookKeyDown.Load() {
				hookKeyDown.Store(false)
			}
			continue
		}
		if c.Key != lastKey || c.Mod != lastMod {
			st = &evdevHotkeyState{}
			lastKey, lastMod = c.Key, c.Mod
		}
		_, modBit := evdevModCodes(c.Mod)
		st.mainCode = code
		st.modBit = modBit
		hit := st.handle(ev.code, ev.value)
		evdevModMask.Store(st.modMask)
		hookKeyDown.Store(hit)
	}
}
