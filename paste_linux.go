//go:build linux

package main

// paste_linux.go - 文本输出（平台函数 emitText，doTranscribe 调用）。
//   Wayland：进程内 GTK 剪贴板（首选，不依赖焦点）→ ydotool 发 Ctrl+V
//     （经 ydotoold + uinput，不依赖合成器协议，GNOME 下可用；
//     需 ydotoold 在跑且用户在 input 组，见 install.sh 3b 节；
//     未装/守护进程未起时命令快速失败，自动走到下一步）
//     → wtype 直接打字（virtual-keyboard 协议，只在 wlroots 系合成器
//     如 Sway/Hyprland 上可用；GNOME 的 Mutter 有意不实现该协议，
//     见 mutter#1974/#4124，wtype 在 GNOME 下恒失败）
//     → 都失败时降级为“仅剪贴板”（返回 errClipboardOnly，
//     调用方据此提示用户按 Ctrl+V），不谎报“已粘贴”、也不误报“失败”。
//     注意：不要用 wl-copy——它需要键盘焦点，后台守护进程场景下
//     永久挂起（实测 timeout 6s 仍不退出），会把转写流程卡死。
//     所有子进程调用一律带超时，任何打字工具都不许卡死主流程。
//   X11：atotto 写剪贴板 + xdotool 发 Ctrl+V。
//   工具缺失时返回清晰错误，不静默失败。
import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/atotto/clipboard"
)

func emitText(text string) error {
	if linuxSessionType() == "wayland" {
		return emitTextWayland(text)
	}
	return emitTextX11(text)
}

func emitTextWayland(text string) error {
	// 1) 先写剪贴板（进程内 GTK，不依赖焦点、不起子进程、不会挂起）。
	//    托盘就绪时该调用必定生效，是 GNOME 下的降级底线。
	clipboardOK := false
	if gtkReady.Load() {
		gtkClipboardSet(text)
		clipboardOK = true
	} else {
		logf("托盘未就绪，GTK 剪贴板不可用，改试 wl-copy（带超时）")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "wl-copy")
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			logf("wl-copy 写剪贴板失败: %v", err)
		} else {
			clipboardOK = true
		}
	}
	// 2) 再试 ydotool 发 Ctrl+V（经 ydotoold + uinput，不依赖合成器协议，
	//    GNOME 下可用）。与 X11 路径一致，剪贴板写完先停 30ms，
	//    避免目标应用在剪贴板就绪前消费 Ctrl+V。
	//    ydotool 未装或守护进程未起时命令快速失败，自动走到 wtype。
	time.Sleep(30 * time.Millisecond)
	ydCtx, ydCancel := context.WithTimeout(context.Background(), 10*time.Second)
	ydOut, ydErr := exec.CommandContext(ydCtx, "ydotool", "key", "ctrl+v").CombinedOutput()
	ydCancel()
	if ydErr == nil {
		return nil
	}
	logf("ydotool 粘贴失败，改试 wtype: %s", strings.TrimSpace(string(ydOut)))
	// 3) 再试 wtype 直接打字（只在 wlroots 系合成器上可用；
	//    GNOME/Mutter 不实现 virtual-keyboard 协议，此处恒失败）。
	//    子进程一律带超时：任何打字工具都不许把转写流程卡死
	//    （此前 wl-copy 无超时挂起曾导致整条转写链路僵死）。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wtype", text)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if clipboardOK {
			// 文本已在剪贴板，调用方提示用户手动 Ctrl+V，不算输出失败。
			logf("wtype 打字失败（已降级为仅剪贴板）: %s", strings.TrimSpace(string(out)))
			return errClipboardOnly
		}
		return fmt.Errorf("wtype 打字失败（是否未安装 wtype？输出：%s）: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func emitTextX11(text string) error {
	var clipErr error
	for retry := 0; retry < 3; retry++ {
		clipErr = clipboard.WriteAll(text)
		if clipErr == nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if clipErr != nil {
		return clipErr
	}
	time.Sleep(30 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "xdotool", "key", "ctrl+v").CombinedOutput()
	if err != nil {
		return fmt.Errorf("xdotool 粘贴失败（是否未安装 xdotool？输出：%s）: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
