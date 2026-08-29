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
# PowerShell 运行
irm https://raw.githubusercontent.com/lisiyu/voice2text-go/main/install.ps1 | iex
```

或下载 `install.ps1` 后右键"使用 PowerShell 运行"。

安装脚本会：
1. 检测系统硬件（NPU/GPU/CPU）
2. 自动下载所需依赖文件
3. 编译或下载预编译二进制
4. 创建桌面快捷方式
5. 启动应用

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

### 方式三：手动安装

1. 从 [Releases](https://github.com/lisiyu/voice2text-go/releases) 下载最新版本
2. 解压到任意目录
3. 确保 `bin/` 目录包含 whisper-server 和模型文件
4. 运行 `voice2text.exe`

## 配置说明

配置文件自动创建在 `%AppData%\voice2text.json`：

```json
{
  "key": "space",
  "mod": "",
  "hold_ms": 300,
  "api": "http://localhost:13305/api/v1/audio/transcriptions",
  "whisper_server_exe": "C:\\path\\to\\whisper-server.exe",
  "whisper_server_url": "http://127.0.0.1:8080/inference",
  "whisper_server_port": 8080,
  "whisper_model": "C:\\path\\to\\ggml-large-v3-turbo.bin",
  "language": "zh",
  "prompt": "以下是语音转写内容，使用简体中文，英文单词保持原文不要翻译，直接输出。",
  "warn_recording_sec": 90,
  "max_recording_sec": 0,
  "split_long_audio": true,
  "auto_backend": true
}
```

### 配置项说明

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `key` | string | `space` | 触发录音的快捷键 |
| `mod` | string | `""` | 修饰键（ctrl/alt/shift） |
| `hold_ms` | int | `300` | 长按触发时间（毫秒） |
| `whisper_server_exe` | string | - | whisper-server 可执行文件路径 |
| `whisper_model` | string | - | Whisper 模型文件路径 |
| `whisper_server_port` | int | `8080` | whisper-server 端口 |
| `language` | string | `zh` | 转写语言 |
| `prompt` | string | - | 转写提示词（影响输出格式） |
| `warn_recording_sec` | int | `90` | 录音超长警告时间 |
| `max_recording_sec` | int | `0` | 录音硬上限（0=不限） |
| `split_long_audio` | bool | `true` | 自动切分长音频 |
| `auto_backend` | bool | `true` | 自动探测最佳后端 |

## 快捷键

右键托盘图标可：
- 切换预设快捷键（Space/F8/F9/RightCtrl/CapsLock/Home/End/Insert）
- 自定义快捷键（点击后按下任意键）
- 编辑配置（记事本打开）
- 退出程序

## 硬件支持

### AMD Ryzen AI NPU

如果你的 CPU 是 AMD Ryzen AI 系列（如 Ryzen AI 7/9, Ryzen AI MAX+），程序会自动检测并使用 NPU 加速：

- 使用 Vitis AI 编码器（encoder）在 NPU 上运行
- 解码器（decoder）在 CPU 上运行
- 比纯 CPU 快 2-3 倍

### GPU (Vulkan)

如果系统有支持 Vulkan 的 GPU，会自动回退到 GPU 加速。

### CPU

兜底方案，支持所有 x64 Windows 系统。

## 常见问题

### Q: 启动后没有托盘图标？

检查 `%TEMP%\voice2text.log` 查看错误信息。可能是：
- 缺少运行时 DLL
- whisper-server 启动失败
- 端口被占用

### Q: 录音后没有转写结果？

检查 whisper-server 是否正常运行：
```powershell
Invoke-WebRequest -Uri "http://127.0.0.1:8080/health"
```

### Q: 如何更换模型？

1. 下载 GGML 格式的 Whisper 模型
2. 修改配置文件中的 `whisper_model` 路径
3. 重启程序

### Q: 如何使用远程 API？

将 `whisper_server_exe` 设为空，配置 `api` 字段指向兼容的 API 端点。

## 技术架构

```
voice2text-go
├── main.go          # 主程序：热键检测、转写流程、配置管理
├── audio.go         # Windows 波形录音（waveIn API）
├── overlay.go       # 悬浮窗 UI（Win32 原生 API）
├── tray_win.go      # 系统托盘（Win32 原生 API）
├── backend.go       # 后端探测与管理（NPU/GPU/CPU）
└── icon_png.go      # 程序化生成麦克风图标
```

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
| [onnxruntime](https://github.com/microsoft/onnxruntime) | Microsoft | MIT | NPU 推理运行时 |
| [Vitis AI](https://github.com/Xilinx/Vitis-AI) | AMD/Xilinx | Apache-2.0 | AMD NPU 编译与运行时 |
| [lemonade](https://github.com/lemonade-sdk/lemonade) | Lemonade | Apache-2.0 | NPU 后端集成参考 |

### Go 生态

| 项目 | 作者 | 许可证 | 用途 |
|------|------|--------|------|
| [golang.org/x/sys/windows](https://pkg.go.dev/golang.org/x/sys/windows) | The Go Authors | BSD-3 | Windows API 调用 |

### 特别感谢

- **[ggerganov/whisper.cpp](https://github.com/ggerganov/whisper.cpp)** — 本项目的核心推理引擎，提供了高效的 Whisper C++ 实现
- **[AMD/Xilinx Vitis AI](https://github.com/Xilinx/Vitis-AI)** — 为 AMD Ryzen AI NPU 提供了编译与运行时支持
- **[lemonade-sdk](https://github.com/lemonade-sdk/lemonade)** — 提供了 NPU 后端集成的参考实现

## 许可证

MIT License

```
MIT License

Copyright (c) 2026 lisiyu

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
