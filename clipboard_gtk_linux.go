//go:build linux

package main

// clipboard_gtk_linux.go - 进程内 GTK 剪贴板（Wayland 下的首选剪贴板路径）。
//
// 为什么不用 wl-copy：wl-copy/wl-paste 需要键盘焦点才能完成选择
// （它建一个 1x1 隐藏窗口等焦点），后台守护进程场景下焦点永远不到，
// 实测会永久挂起（timeout 6s 仍不退出），进而把转写流程卡死。
// 而 GTK 剪贴板（gtk_clipboard_set_text）是剪贴板管理器的做法：
// 拥有选择不需要焦点，用户在任何窗口按 Ctrl+V 时，
// GTK 主循环会响应数据请求把文本交出去。
// 调用通过 g_idle_add 投递到 GTK 主循环线程（与 systray 同款做法），
// 调用方不阻塞等待——托盘就绪（gtkReady）后调用即有效。

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>
#include <stdlib.h>

// 在 GTK 主循环线程上执行：取剪贴板并设置文本，随后释放内存，只跑一次。
static gboolean do_set_clipboard(gpointer data) {
	char *text = (char *)data;
	GtkClipboard *cb = gtk_clipboard_get(GDK_SELECTION_CLIPBOARD);
	if (cb != NULL) {
		gtk_clipboard_set_text(cb, text, -1);
		gtk_clipboard_store(cb);
	}
	free(text);
	return FALSE;
}

static void set_clipboard_async(const char *text) {
	g_idle_add(do_set_clipboard, (gpointer)strdup(text));
}
*/
import "C"

import (
	"sync/atomic"
	"unsafe"
)

// gtkReady：托盘 onReady 已跑（即 GTK 主循环已启动），此时剪贴板调用有效。
var gtkReady atomic.Bool

func gtkClipboardSet(text string) {
	ctext := C.CString(text)
	defer C.free(unsafe.Pointer(ctext))
	C.set_clipboard_async(ctext)
}
