#!/bin/bash
# voice2text Linux 安装脚本（对应 Windows 的 install.ps1）。
#
# 用法：
#   ./install.sh [安装目录，默认 ~/voice2text]
#
# 说明：
# - 本脚本不编译 Go（交付包里已含预编译的 voice2text 与 whisper 后端），
#   只负责：依赖检查、wtype/wl-clipboard 安装、文件落位、模型获取、
#   配置写入、自启动与 GNOME 快捷键绑定。
# - 幂等：重复运行会复用已有模型/配置，只补缺失项。
# - sudo 失败时不会静默跳过：先尝试 sudo apt 安装，失败则改用
#   apt-get download + dpkg-deb -x 免 root 安装到 ~/.local/bin；
#   两条路都失败时报错退出并给出手动命令。
set -u

INSTALL_DIR="${1:-$HOME/voice2text}"
PKG_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$INSTALL_DIR/bin"
TOOLS_BIN="$HOME/.local/bin"

fail() { echo "[失败] $*" >&2; exit 1; }
info() { echo "[安装] $*"; }

# ---------- 1. 会话环境补齐（从桥上跑本脚本时尤其重要） ----------
UID_NOW="$(id -u)"
if [ -z "${DBUS_SESSION_BUS_ADDRESS:-}" ] && [ -S "/run/user/$UID_NOW/bus" ]; then
    export DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$UID_NOW/bus"
fi
if [ -z "${XDG_RUNTIME_DIR:-}" ] && [ -d "/run/user/$UID_NOW" ]; then
    export XDG_RUNTIME_DIR="/run/user/$UID_NOW"
fi
if [ -z "${WAYLAND_DISPLAY:-}" ] && [ -S "/run/user/$UID_NOW/wayland-0" ]; then
    export WAYLAND_DISPLAY="wayland-0"
fi

# 免 root 回退会把 wtype/wl-copy 装到 ~/.local/bin；桥上跑时该目录
# 不在默认 PATH 里，先补上，避免每次重装都误判为缺失而重复下载。
export PATH="$HOME/.local/bin:$PATH"

# ---------- 2. 必需工具检查 ----------
for t in pw-record notify-send gsettings curl python3; do
    command -v "$t" >/dev/null 2>&1 || fail "缺少必需工具 $t（pw-record 属 pipewire 包，notify-send 属 libnotify-bin）"
done
info "必需工具齐全：pw-record / notify-send / gsettings / curl / python3"

# ---------- 3. wtype + wl-clipboard（Wayland 下打字与剪贴板必需） ----------
need_tools=""
command -v wtype >/dev/null 2>&1 || need_tools="$need_tools wtype"
command -v wl-copy >/dev/null 2>&1 || need_tools="$need_tools wl-copy"
if [ -n "$need_tools" ]; then
    info "缺少：$need_tools，尝试安装（包名：wl-clipboard wtype）"
    installed=""
    if sudo -n true 2>/dev/null; then
        if sudo apt-get install -y wl-clipboard wtype 2>&1 | tail -1; then
            installed="apt"
        fi
    else
        info "sudo 免密不可用（桥上常见：NoNewPrivileges），改用免 root 方式"
    fi
    if [ "$installed" != "apt" ]; then
        # 免 root 回退：apt-get download（无需 root）+ dpkg-deb -x 解包到 ~/.local/bin
        tmpd="$(mktemp -d)"
        if (cd "$tmpd" && apt-get download wl-clipboard wtype >/dev/null 2>&1); then
            mkdir -p "$TOOLS_BIN"
            for deb in "$tmpd"/*.deb; do
                dpkg-deb -x "$deb" "$tmpd/pkg" 2>/dev/null
                [ -x "$tmpd/pkg/usr/bin/wtype" ] && cp "$tmpd/pkg/usr/bin/wtype" "$TOOLS_BIN/"
                [ -x "$tmpd/pkg/usr/bin/wl-copy" ] && cp "$tmpd/pkg/usr/bin/wl-copy" "$TOOLS_BIN/"
                [ -x "$tmpd/pkg/usr/bin/wl-paste" ] && cp "$tmpd/pkg/usr/bin/wl-paste" "$TOOLS_BIN/"
                rm -rf "$tmpd/pkg"
            done
            rm -rf "$tmpd"
            export PATH="$TOOLS_BIN:$PATH"
        fi
    fi
    command -v wtype >/dev/null 2>&1 || fail "wtype 仍未安装。请在自己的终端里手动执行：sudo apt-get install -y wl-clipboard wtype"
    command -v wl-copy >/dev/null 2>&1 || fail "wl-copy 仍未安装。请在自己的终端里手动执行：sudo apt-get install -y wl-clipboard wtype"
    info "wtype / wl-copy 已就绪"
fi

# ---------- 4. 文件落位 ----------
mkdir -p "$BIN_DIR"
[ -f "$PKG_DIR/voice2text" ] || fail "交付包里没有 voice2text 主程序（应在脚本同目录）"
cp "$PKG_DIR/voice2text" "$INSTALL_DIR/voice2text"
chmod +x "$INSTALL_DIR/voice2text"
for f in whisper-server whisper-cli; do
    if [ -f "$PKG_DIR/bin/$f" ]; then
        cp "$PKG_DIR/bin/$f" "$BIN_DIR/$f"
        chmod +x "$BIN_DIR/$f"
    fi
done
# whisper 动态库（.so 与主程序同目录，rpath 已设为 $ORIGIN，随包走）
if ls "$PKG_DIR"/bin/lib*.so* >/dev/null 2>&1; then
    cp "$PKG_DIR"/bin/lib*.so* "$BIN_DIR/" 2>/dev/null || true
fi
[ -x "$BIN_DIR/whisper-server" ] || fail "whisper-server 未落位（交付包 bin/ 里应有 whisper-server）"
info "文件已落位：$INSTALL_DIR"

# ---------- 5. 模型 ----------
MODEL="$BIN_DIR/ggml-large-v3-turbo.bin"
if [ ! -s "$MODEL" ]; then
    info "下载模型 ggml-large-v3-turbo.bin（约 1.6GB）..."
    curl -L -o "$MODEL" "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin" \
        || fail "模型下载失败，请检查网络后重跑本脚本（已下载部分会被断点续传覆盖重下）"
fi
[ -s "$MODEL" ] || fail "模型文件为空：$MODEL"
info "模型就绪：$MODEL"

# ---------- 6. 配置写入（保留已有 key/mod，只补后端路径） ----------
CFG="$INSTALL_DIR/voice2text.json"
python3 - "$CFG" "$BIN_DIR/whisper-server" "$MODEL" "$BIN_DIR/whisper-cli" << 'PYEOF'
import json, os, sys
cfg_path, server_exe, model, cli = sys.argv[1:5]
cfg = {}
if os.path.exists(cfg_path):
    try:
        with open(cfg_path, encoding="utf-8") as f:
            cfg = json.load(f)
    except Exception as e:
        print(f"[安装] 旧配置解析失败，已备份后重建：{e}")
        os.replace(cfg_path, cfg_path + ".bak")
        cfg = {}
cfg.setdefault("key", "space")
cfg.setdefault("mod", "")
cfg.setdefault("hold_ms", 300)
cfg["whisper_server_exe"] = server_exe
cfg["whisper_model"] = model
cfg["whisper_cli"] = cli
cfg["whisper_server_url"] = "http://127.0.0.1:18080/inference"
cfg["whisper_server_port"] = 18080
cfg["auto_backend"] = True
with open(cfg_path, "w", encoding="utf-8") as f:
    json.dump(cfg, f, ensure_ascii=False, indent=2)
print(f"[安装] 配置已写入：{cfg_path}")
PYEOF

# ---------- 7. 自启动（.desktop，不依赖 user systemd——桥进程连不上 user bus） ----------
mkdir -p "$HOME/.config/autostart"
cat > "$HOME/.config/autostart/voice2text.desktop" << EOF
[Desktop Entry]
Type=Application
Name=voice2text
Comment=语音转文字（按住热键说话，松开转写粘贴；Wayland 下用快捷键 toggle）
Exec=sh -c 'PATH=$TOOLS_BIN:$BIN_DIR:\$PATH exec $INSTALL_DIR/voice2text'
Terminal=false
X-GNOME-Autostart-enabled=true
EOF
# 注意：.desktop 的 Exec 行不做 shell 变量展开，所以上面用 sh -c 包一层，
# 让 PATH 在启动时由 shell 展开（\$PATH 在此 heredoc 里写成字面量进文件）。
info "自启动已写入：~/.config/autostart/voice2text.desktop"

# ---------- 7b. 托盘扩展启用（GNOME 顶栏显示托盘图标的前提） ----------
# ubuntu-appindicators 默认随系统安装但未必启用；未启用时托盘图标不显示。
# Wayland 下新启用的扩展要到下次登录才载入——本步只负责启用，不报错中断安装。
if command -v gnome-extensions >/dev/null 2>&1; then
    if gnome-extensions enable ubuntu-appindicators@ubuntu.com 2>/dev/null; then
        info "托盘扩展 ubuntu-appindicators 已启用（Wayland 下需注销重登录后载入，届时托盘图标出现）"
    else
        info "托盘扩展启用失败（不影响安装）：请在“扩展”应用里手动启用 Ubuntu AppIndicators，注销重登录后生效"
    fi
fi

# ---------- 8. GNOME 自定义快捷键绑定（Wayland 下的触发方式） ----------
# 注意：custom-keybindings 是整列表，追加时必须保留已有项，不能覆盖。
KEYBIND_PATH="/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/voice2text/"
CUR="$(gsettings get org.gnome.settings-daemon.plugins.media-keys custom-keybindings 2>/dev/null || echo "@as []")"
if ! echo "$CUR" | grep -q "voice2text/"; then
    if [ "$CUR" = "@as []" ] || [ "$CUR" = "[]" ]; then
        NEW="['$KEYBIND_PATH']"
    else
        NEW="${CUR%]*}, '$KEYBIND_PATH']"
    fi
    gsettings set org.gnome.settings-daemon.plugins.media-keys custom-keybindings "$NEW" \
        || fail "gsettings 写入 custom-keybindings 失败（桥上跑时需 DBUS_SESSION_BUS_ADDRESS 指向用户会话）"
fi
gsettings set org.gnome.settings-daemon.plugins.media-keys.custom-keybinding:$KEYBIND_PATH name "voice2text 语音输入" \
    || fail "gsettings 写入快捷键名称失败"
gsettings set org.gnome.settings-daemon.plugins.media-keys.custom-keybinding:$KEYBIND_PATH command "$INSTALL_DIR/voice2text toggle" \
    || fail "gsettings 写入快捷键命令失败"
gsettings set org.gnome.settings-daemon.plugins.media-keys.custom-keybinding:$KEYBIND_PATH binding "<Control><Alt>v" \
    || fail "gsettings 写入快捷键组合失败"
info "GNOME 快捷键已绑定：Ctrl+Alt+V → $INSTALL_DIR/voice2text toggle"

echo
echo "[完成] 安装结束。启动与验证："
echo "  手动启动：$INSTALL_DIR/voice2text"
echo "  看日志：  tail -f /tmp/voice2text.log"
echo "  触发测试：按 Ctrl+Alt+V 开始录音，再按一次停止并转写"
echo "  下次登录后自启动生效。"
