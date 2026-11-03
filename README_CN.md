# voice2text-go

**Windows 语音转文字工具** — 按住快捷键说话，松开自动转写并粘贴到当前输入框。

基于 Go + Windows 原生 API 实现，支持 AMD Ryzen AI NPU / GPU / CPU 多后端自动探测。

## 功能特性

- **一键语音输入**：长按快捷键（默认 Space 300ms）录音，松开自动转写并粘贴
- **NPU 加速**：自动检测 AMD Ryzen AI XDNA NPU，使用 Vitis AI 编码器加速推理
- **多后端回退**：NPU → GPU(Vulkan) → CPU，自动探测最佳可用后端
- **实时悬浮窗**：录音时显示波形动画，转写中显示呼吸灯，结果一闪即逝
- **零配置**：单文件运行，配置文件自动热重载
- **上下文隔离**：每次转写后自动重启 whisper-server，杜绝空格/幻觉污染

## 系统要求

| 项目 | 要求 |
|------|------|
| 操作系统 | Windows 10/11 (x64) |
| Go | 1.21+ (仅编译需要) |
| RAM | 8GB+ (推荐 16GB+) |
| NPU | AMD Ryzen AI 系列 (可选，自动检测) |
| 磁盘 | ~2GB (模型文件) |

## 快速开始

### 方式一：一键安装（推荐）

```powershell
# PowerShell 管理员运行
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass
.\install.ps1
```

安装脚本会：
1. 检测系统硬件（NPU/GPU/CPU）
2. 自动下载所需依赖文件
3. 生成配置文件
4. 创建桌面快捷方式

### 方式二：从源码编译

```bash
# 克隆仓库
git clone https://github.com/lisiyu/voice2text-go.git
cd voice2text-go

# 编译
go build -ldflags="-H windowsgui" -o voice2text.exe .

# 运行
.\voice2text.exe
```

## Linux 版

已在 Ubuntu 26.04 + GNOME 50.1 + Wayland 真机上实测通过（2026-11-03）。

### 系统要求

| 项目 | 要求 |
|------|------|
| 操作系统 | Linux x64（GNOME + Wayland 为主路径；X11 亦支持，见下） |
| 音频 | PipeWire（`pw-record`） |
| 托盘 | GTK 3 + AppIndicator 扩展 |
| 磁盘 | ~2GB（模型文件约 1.6GB） |

### 安装

交付包里已含预编译的 `voice2text` 与 whisper 后端（目标机一般无 Go，
安装脚本不编译，只负责落位与配置）：

```bash
# 解压交付包后，在包目录执行（默认装到 ~/voice2text，可传参改目录）
./install.sh
```

安装脚本会：
1. 检查必需工具（`pw-record`、`gsettings`、`curl`、`python3` 等）
2. 安装 `wtype` 与 `wl-clipboard`（先试 `sudo apt`，失败则免 root
   装到 `~/.local/bin`；两条路都失败时报错退出，不静默跳过）
3. 把程序与 `bin/`（whisper-server、whisper-cli、动态库）落位
4. 模型缺失时从 HuggingFace 下载 `ggml-large-v3-turbo.bin`（已存在则复用）
5. 写入配置（保留已有的 `key`/`mod`，只补后端路径）
6. 写入自启动 `~/.config/autostart/voice2text.desktop`
7. 绑定 GNOME 自定义快捷键 **Ctrl+Alt+V** → `voice2text toggle`，
   并启用 AppIndicator 托盘扩展（如未启用）

### 用法（Wayland）

- 按 **Ctrl+Alt+V** 开始录音，再按一次停止并转写（toggle 语义，
  因为桌面快捷键给不了“按住/松开”语义）。
- 也可以直接跑 `voice2text toggle` 触发开始/停止。
- 转写完成后托盘状态与桌面通知提示“已复制到剪贴板，请按 Ctrl+V 粘贴”，
  在目标输入框按 **Ctrl+V** 粘贴。

### 用法（X11）

X11 会话下用全局热键（默认 f8，长按录音、松开转写，与 Windows 一致），
转写后自动发 Ctrl+V 粘贴。**注意：X11 路径未在真机验证**（真机为 Wayland），
X11 用户请按同样流程验收一次。

### 托盘图标

Wayland 下新启用的 AppIndicator 扩展要到下次登录才载入：
安装后请**注销重登录**，登录后托盘图标出现（自启动会拉起程序）。

### 已知限制（GNOME / Wayland）

- **自动打字不可用**：GNOME 的 Mutter 有意不实现 virtual-keyboard 协议，
  `wtype` 在此恒失败；因此转写结果走“剪贴板 + 手动 Ctrl+V”降级，
  不谎报已粘贴。如需真自动打字，须走 uinput/ydotool 路线
  （一次性 sudo：udev 规则放行 `/dev/uinput` 或把用户加入 input 组，
  再装 ydotool 并跑 ydotoold 守护进程）——本任务未实现，需用户拍板。
- **悬浮窗波形不复刻**：Linux 版改为托盘状态 + 桌面通知。
- **不需要 api_key**：Linux 版只调本地模型，配置字段保留做兼容。
- **后端端口默认 18080**（Windows 版为 8080）：避开 Linux 下 llama-server
  常驻的 8080；探活会校验 `Server: whisper.cpp` 头，不会误认 llama-server。

### 故障排查

- 应用日志：`/tmp/voice2text.log`
- 后端日志：`/tmp/whisper-server.log`
- 健康检查：`curl http://127.0.0.1:18080/health`（响应头应为 `Server: whisper.cpp`）
- 触发 socket：`/run/user/1000/voice2text.sock`（`voice2text toggle` 拨它）

## 配置说明

配置文件自动创建在 `%AppData%\voice2text.json`。

右键托盘图标可切换快捷键、编辑配置、退出程序。

## 硬件支持

| 后端 | 检测条件 | 加速方式 |
|------|----------|----------|
| NPU | AMD Ryzen AI CPU | Vitis AI encoder on NPU |
| GPU | 支持 Vulkan 的显卡 | Vulkan compute |
| CPU | 所有 x64 系统 | 多线程 CPU |

## 开源致谢

本项目依赖以下优秀的开源项目：

### 核心依赖

| 项目 | 作者 | 许可证 | 用途 |
|------|------|--------|------|
| [whisper.cpp](https://github.com/ggerganov/whisper.cpp) | Georgi Gerganov | MIT | Whisper 语音识别 C++ 实现 |
| [Go](https://go.dev/) | The Go Authors | BSD-3 | 主开发语言 |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | The Go Authors | BSD-3 | Windows 系统调用 |
| [atotto/clipboard](https://github.com/atotto/clipboard) | Atsushi Togo | BSD-3 | 剪贴板操作 |

### Whisper 模型

| 模型 | 作者 | 许可证 | 说明 |
|------|------|--------|------|
| [whisper.cpp](https://github.com/ggerganov/whisper.cpp) | Georgi Gerganov | MIT | 模型格式与推理引擎 |
| [Whisper](https://github.com/openai/whisper) | OpenAI | MIT | 原始模型架构与权重 |

### NPU 加速

| 项目 | 作者 | 许可证 | 用途 |
|------|------|--------|------|
| [Vitis AI](https://github.com/Xilinx/Vitis-AI) | AMD/Xilinx | Apache-2.0 | AMD NPU 编译与运行时 |
| [lemonade](https://github.com/lemonade-sdk/lemonade) | Lemonade | Apache-2.0 | NPU 后端集成参考 |

### 特别感谢

- **[ggerganov/whisper.cpp](https://github.com/ggerganov/whisper.cpp)** — 本项目的核心推理引擎
- **[AMD/Xilinx Vitis AI](https://github.com/Xilinx/Vitis-AI)** — 为 AMD Ryzen AI NPU 提供编译与运行时支持
- **[lemonade-sdk](https://github.com/lemonade-sdk/lemonade)** — 提供了 NPU 后端集成的参考实现

## 许可证

MIT License - 详见 [LICENSE](LICENSE)
