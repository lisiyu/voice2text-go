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
