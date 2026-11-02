//go:build linux

package main

// socket_linux.go - Wayland 模式的触发通道。
// GNOME 自定义快捷键无法表达“按住/松开”语义，
// 因此 Wayland 下用“按一下开始、再按一下停止”（toggle）：
// 桌面快捷键绑 `voice2text toggle`，子命令经 Unix socket
// 通知主进程，主进程把 hookKeyDown 脉冲一次，
// hotkeyLoop 的 toggleMode 分支据此启停录音。
import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// socketPath：优先 XDG_RUNTIME_DIR，否则 /tmp 下按 uid 隔离。
func socketPath() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "voice2text.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("voice2text-%d.sock", os.Getuid()))
}

// socketListener 监听 toggle 请求（Wayland 模式由 startHotkeyLayer 启动）。
func socketListener() {
	path := socketPath()
	// 若已有实例在监听，不重复监听（双实例由 flock 单实例锁兜底，这里只防 socket 冲突）。
	if conn, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
		conn.Close()
		logf("socket 已有实例在监听: %s", path)
		return
	}
	_ = os.Remove(path) // 清理上次异常退出遗留的 socket 文件
	ln, err := net.Listen("unix", path)
	if err != nil {
		logf("socket 监听失败 %s: %v", path, err)
		return
	}
	logf("socket 已监听: %s", path)
	for {
		c, err := ln.Accept()
		if err != nil {
			logf("socket accept 失败: %v", err)
			return
		}
		go handleSocketConn(c)
	}
}

func handleSocketConn(c net.Conn) {
	defer c.Close()
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return
	}
	if strings.TrimSpace(line) == "toggle" {
		pulseToggle()
		fmt.Fprintln(c, "ok")
	} else {
		fmt.Fprintln(c, "unknown-command")
	}
}

// pulseToggle 把 hookKeyDown 脉冲一次（真→150ms→假），
// hotkeyLoop 的 toggleMode 分支在上升沿启停录音。
func pulseToggle() {
	hookKeyDown.Store(true)
	time.Sleep(150 * time.Millisecond)
	hookKeyDown.Store(false)
}

// toggleCommandMain 是 `voice2text toggle` 子命令的入口（main() 调用）。
func toggleCommandMain() int {
	conn, err := net.DialTimeout("unix", socketPath(), 2*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "连不上 voice2text 主进程（是否未启动？socket=%s）: %v\n", socketPath(), err)
		return 1
	}
	defer conn.Close()
	fmt.Fprintln(conn, "toggle")
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, _ := bufio.NewReader(conn).ReadString('\n')
	resp = strings.TrimSpace(resp)
	if resp != "ok" {
		fmt.Fprintf(os.Stderr, "toggle 未被确认（回执=%q）\n", resp)
		return 1
	}
	fmt.Println("toggle 已发送")
	return 0
}
