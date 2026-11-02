//go:build linux

package main

// tray_linux.go - Linux 托盘（getlantern/systray，AppIndicator）。
// 菜单结构与 Windows 版托盘对齐：状态行、预设热键（当前项打勾）、
// 自定义快捷键、编辑配置、退出。
// 注意：systray.Run 在 Linux 下可在 goroutine 里跑（与 Windows 版
// go runTray() 的非阻塞 startTray 语义一致）；
// 退出时 quitCh 由共享 startTray 的 onQuit 关闭，
// 这里的 onExit 只记日志，避免重复关闭 channel 导致 panic。
import (
	"sync"

	"github.com/getlantern/systray"
)

// 托盘回调（与 tray_windows.go 的同名类型一致，构建标签互斥故不冲突）。
type trayCallbacks struct {
	onKey    func(string)
	onCustom func()
	onAPI    func() // 编辑配置（xdg-open 打开 voice2text.json 本体）
	onQuit   func()
}

var trayCB trayCallbacks

var (
	trayMu       sync.Mutex
	trayStatus   string
	trayStatusIt *systray.MenuItem
	trayCustomIt *systray.MenuItem
	trayKeyItems = map[string]*systray.MenuItem{}
)

var trayPresetKeys = []struct {
	name  string
	label string
}{
	{"space", "空格 (Space)"},
	{"f8", "F8"},
	{"f9", "F9"},
	{"rightctrl", "右 Ctrl"},
	{"leftctrl", "左 Ctrl"},
	{"capslock", "CapsLock"},
	{"home", "Home"},
	{"end", "End"},
	{"insert", "Insert"},
}

func trayOnReady() {
	// GTK 主循环已启动：此后 gtkClipboardSet 投递的调用能被执行。
	gtkReady.Store(true)
	systray.SetIcon(genMicPNG())
	systray.SetTitle("voice2text")
	systray.SetTooltip("语音输入 voice2text")

	trayStatusIt = systray.AddMenuItem("状态：空闲", "")
	trayStatusIt.Disable()
	systray.AddSeparator()
	for _, p := range trayPresetKeys {
		it := systray.AddMenuItem(p.label, "")
		trayKeyItems[p.name] = it
		name := p.name
		go func() {
			for range it.ClickedCh {
				if trayCB.onKey != nil {
					trayCB.onKey(name)
				}
			}
		}()
	}
	trayCustomIt = systray.AddMenuItem("自定义快捷键...", "")
	go func() {
		for range trayCustomIt.ClickedCh {
			if trayCB.onCustom != nil {
				trayCB.onCustom()
			}
		}
	}()
	systray.AddSeparator()
	apiIt := systray.AddMenuItem("编辑配置...", "")
	go func() {
		for range apiIt.ClickedCh {
			if trayCB.onAPI != nil {
				trayCB.onAPI()
			}
		}
	}()
	systray.AddSeparator()
	quitIt := systray.AddMenuItem("退出", "")
	go func() {
		for range quitIt.ClickedCh {
			if trayCB.onQuit != nil {
				trayCB.onQuit()
			}
		}
	}()
	refreshKeyChecks()
}

func trayOnExit() {
	logf("托盘已退出")
}

// refreshKeyChecks 按当前配置给预设热键打勾；
// 当前热键不在预设列表时，自定义项显示“当前: X (点击修改)”。
func refreshKeyChecks() {
	cur := currentKeyName()
	for name, it := range trayKeyItems {
		if name == cur {
			it.Check()
		} else {
			it.Uncheck()
		}
	}
	if trayCustomIt == nil {
		return
	}
	preset := false
	for _, p := range trayPresetKeys {
		if p.name == cur {
			preset = true
			break
		}
	}
	if !preset && cur != "" {
		trayCustomIt.SetTitle("当前: " + cur + " (点击修改)")
	} else {
		trayCustomIt.SetTitle("自定义快捷键...")
	}
}

// ---------- 对外接口（与 Windows 版托盘同名） ----------
func initTray(cb trayCallbacks) {
	trayCB = cb
	go systray.Run(trayOnReady, trayOnExit)
}

func updateTrayStatus(t string) {
	trayMu.Lock()
	trayStatus = t
	statusIt := trayStatusIt
	trayMu.Unlock()
	if statusIt != nil {
		statusIt.SetTitle(t)
	}
	systray.SetTooltip("voice2text - " + t)
}

func quitTray() {
	systray.Quit()
}
