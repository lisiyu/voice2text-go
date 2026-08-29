#Requires -RunAsAdministrator
<#
.SYNOPSIS
    voice2text-go 一键安装脚本
.DESCRIPTION
    自动检测系统硬件（NPU/GPU/CPU），下载所需依赖，安装并配置语音转文字工具。
.NOTES
    以管理员权限运行：右键 install.ps1 -> 使用 PowerShell 运行
#>

param(
    [string]$InstallDir = "$env:LOCALAPPDATA\voice2text-go",
    [string]$ModelDir = "$env:LOCALAPPDATA\voice2text-go\models",
    [switch]$SkipDownload,
    [switch]$Force
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

# ============================================================
# 工具函数
# ============================================================
function Write-Step {
    param([string]$Message)
    Write-Host "`n[$([char]0x25B6)] $Message" -ForegroundColor Cyan
}

function Write-OK {
    param([string]$Message)
    Write-Host "  [OK] $Message" -ForegroundColor Green
}

function Write-Warn {
    param([string]$Message)
    Write-Host "  [!] $Message" -ForegroundColor Yellow
}

function Write-Fail {
    param([string]$Message)
    Write-Host "  [X] $Message" -ForegroundColor Red
}

function Write-Info {
    param([string]$Message)
    Write-Host "  $Message" -ForegroundColor Gray
}

function Test-Command {
    param([string]$Command)
    $null -ne (Get-Command $Command -ErrorAction SilentlyContinue)
}

function Get-FileHashSHA256 {
    param([string]$Path)
    (Get-FileHash -Path $Path -Algorithm SHA256).Hash
}

function Download-File {
    param(
        [string]$Url,
        [string]$OutFile,
        [string]$Description
    )
    if (Test-Path $OutFile) {
        Write-Info "$Description 已存在，跳过下载"
        return $true
    }
    Write-Info "下载 $Description ..."
    Write-Info "  URL: $Url"
    try {
        $tmpFile = "$OutFile.download"
        Invoke-WebRequest -Uri $Url -OutFile $tmpFile -UseBasicParsing
        Move-Item -Path $tmpFile -Destination $OutFile -Force
        Write-OK "$Description 下载完成"
        return $true
    } catch {
        Write-Fail "$Description 下载失败: $_"
        if (Test-Path $tmpFile) { Remove-Item $tmpFile -Force }
        return $false
    }
}

# ============================================================
# 1. 硬件检测
# ============================================================
Write-Step "检测系统硬件..."

# CPU 信息
$cpu = Get-CimInstance -ClassName Win32_Processor | Select-Object -First 1
$cpuName = $cpu.Name
$cpuCores = $cpu.NumberOfCores
Write-Info "CPU: $cpuName ($cpuCores cores)"

# NPU 检测（AMD Ryzen AI 系列）
$hasNPU = $false
$npuType = ""
if ($cpuName -match "Ryzen\s+AI") {
    $hasNPU = $true
    if ($cpuName -match "MAX\+") {
        $npuType = "AMD Ryzen AI MAX+ (XDNA2)"
    } elseif ($cpuName -match "MAX") {
        $npuType = "AMD Ryzen AI MAX (XDNA)"
    } elseif ($cpuName -match "9\s*870") {
        $npuType = "AMD Ryzen AI 9 870 (XDNA2)"
    } elseif ($cpuName -match "9\s*850") {
        $npuType = "AMD Ryzen AI 9 850 (XDNA2)"
    } elseif ($cpuName -match "7\s*840") {
        $npuType = "AMD Ryzen AI 7 840 (XDNA)"
    } else {
        $npuType = "AMD Ryzen AI (XDNA)"
    }
    Write-OK "检测到 NPU: $npuType"
} else {
    Write-Info "未检测到 AMD Ryzen AI NPU"
}

# GPU 检测
$hasGPU = $false
$gpuName = ""
try {
    $gpus = Get-CimInstance -ClassName Win32_VideoController | Where-Object { $_.Name -notmatch "Microsoft|Basic|Parsec" }
    if ($gpus) {
        $hasGPU = $true
        $gpuName = ($gpus | Select-Object -First 1).Name
        Write-OK "检测到 GPU: $gpuName"
    }
} catch {}
if (-not $hasGPU) {
    Write-Info "未检测到独立 GPU"
}

# RAM
$ramGB = [math]::Round((Get-CimInstance -ClassName Win32_ComputerSystem).TotalPhysicalMemory / 1GB, 1)
Write-Info "内存: ${ramGB} GB"
if ($ramGB -lt 8) {
    Write-Warn "内存不足 8GB，可能影响模型加载速度"
}

# ============================================================
# 2. 依赖检测
# ============================================================
Write-Step "检测系统依赖..."

# Go
$hasGo = Test-Command "go"
if ($hasGo) {
    $goVersion = (go version 2>$null) -replace ".*go(\d+\.\d+\.\d+).*", '$1'
    Write-OK "Go $goVersion"
} else {
    Write-Warn "Go 未安装，将使用预编译二进制"
}

# Git
$hasGit = Test-Command "git"
if ($hasGit) {
    Write-OK "Git"
} else {
    Write-Warn "Git 未安装，部分功能受限"
}

# ============================================================
# 3. 创建目录结构
# ============================================================
Write-Step "创建安装目录..."

$dirs = @($InstallDir, "$InstallDir\bin", $ModelDir)
foreach ($dir in $dirs) {
    if (-not (Test-Path $dir)) {
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
        Write-Info "创建: $dir"
    }
}
Write-OK "目录结构就绪"

# ============================================================
# 4. 下载 whisper-server 和模型
# ============================================================
Write-Step "下载 Whisper 组件..."

$binDir = "$InstallDir\bin"

# whisper-server 后端候选
$backends = @(
    @{
        Name = "NPU (AMD Ryzen AI XDNA)"
        ExeUrl = "https://github.com/ggerganov/whisper.cpp/releases/download/v1.7.5/whisper-bin-x64.zip"
        FileName = "whisper-server.exe"
        Priority = if ($hasNPU) { 0 } else { 99 }
    },
    @{
        Name = "CPU (通用)"
        ExeUrl = "https://github.com/ggerganov/whisper.cpp/releases/download/v1.7.5/whisper-bin-x64.zip"
        FileName = "whisper-server.exe"
        Priority = if ($hasNPU) { 1 } else { 0 }
    }
)

# 按优先级排序
$backends = $backends | Sort-Object { $_.Priority }

# 模型
$modelUrl = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin"
$modelFile = "$ModelDir\ggml-large-v3-turbo.bin"
$modelSizeMB = 1549

# 下载 whisper-server
$exePath = "$binDir\whisper-server.exe"
if (Test-Path $exePath) {
    Write-OK "whisper-server.exe 已存在"
} elseif (-not $SkipDownload) {
    Write-Info "下载 whisper-server..."
    $downloaded = $false
    foreach ($backend in $backends) {
        Write-Info "尝试 $($backend.Name) 后端..."
        $zipUrl = $backend.ExeUrl
        $zipFile = "$binDir\whisper-server.zip"
        
        if (Download-File -Url $zipUrl -OutFile $zipFile -Description "whisper-server ($($backend.Name))") {
            try {
                Expand-Archive -Path $zipFile -DestinationPath $binDir -Force
                Remove-Item $zipFile -Force
                
                # 检查解压后的文件名
                $candidates = @("whisper-server.exe", "main.exe", "whisper.exe")
                foreach ($c in $candidates) {
                    $src = "$binDir\$c"
                    if (Test-Path $src) {
                        if ($c -ne "whisper-server.exe") {
                            Rename-Item -Path $src -NewName "whisper-server.exe" -Force
                        }
                        $downloaded = $true
                        break
                    }
                }
                
                # 清理解压出的多余文件
                Get-ChildItem $binDir -Filter "*.exe" | Where-Object { $_.Name -ne "whisper-server.exe" } | Remove-Item -Force -ErrorAction SilentlyContinue
                Get-ChildItem $binDir -Filter "*.txt" | Remove-Item -Force -ErrorAction SilentlyContinue
                Get-ChildItem $binDir -Filter "*.md" | Remove-Item -Force -ErrorAction SilentlyContinue
                
                break
            } catch {
                Write-Warn "解压失败: $_"
            }
        }
    }
    
    if (-not $downloaded) {
        Write-Fail "无法下载 whisper-server"
        Write-Info "请手动下载 whisper-server 并放置到: $binDir"
    }
}

# 下载模型
if (Test-Path $modelFile) {
    Write-OK "模型文件已存在 ($modelSizeMB MB)"
} elseif (-not $SkipDownload) {
    Write-Info "下载 Whisper Large-v3-Turbo 模型 (~${modelSizeMB}MB)..."
    Write-Info "这可能需要几分钟，请耐心等待..."
    
    $downloaded = Download-File -Url $modelUrl -OutFile $modelFile -Description "Whisper 模型"
    if (-not $downloaded) {
        Write-Fail "模型下载失败"
        Write-Info "请手动下载模型并放置到: $ModelDir"
        Write-Info "下载地址: $modelUrl"
    }
}

# ============================================================
# 5. 生成配置文件
# ============================================================
Write-Step "生成配置文件..."

$configPath = "$env:APPDATA\voice2text.json"
$config = @{
    key = "space"
    mod = ""
    hold_ms = 300
    api = "http://localhost:13305/api/v1/audio/transcriptions"
    model = "Whisper-Large-v3-Turbo"
    api_key = ""
    whisper_server_exe = $exePath.Replace('\', '\\')
    whisper_server_url = "http://127.0.0.1:8080/inference"
    whisper_server_port = 8080
    whisper_cli = ""
    whisper_model = $modelFile.Replace('\', '\\')
    language = "zh"
    prompt = "以下是语音转写内容，使用简体中文，英文单词保持原文不要翻译，直接输出。"
    warn_recording_sec = 90
    max_recording_sec = 0
    split_long_audio = $true
    auto_backend = $true
} | ConvertTo-Json -Depth 10

if ((Test-Path $configPath) -and -not $Force) {
    Write-Warn "配置文件已存在: $configPath"
    Write-Info "使用 -Force 参数覆盖现有配置"
} else {
    $config | Out-File -FilePath $configPath -Encoding UTF8
    Write-OK "配置文件已创建: $configPath"
}

# ============================================================
# 6. 创建快捷方式
# ============================================================
Write-Step "创建快捷方式..."

$desktopPath = [Environment]::GetFolderPath("Desktop")
$shortcutPath = "$desktopPath\voice2text.lnk"
$exeFullPath = if (Test-Path $exePath) { $exePath } else { "$InstallDir\voice2text.exe" }

if (Test-Path $exeFullPath) {
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut($shortcutPath)
    $shortcut.TargetPath = $exeFullPath
    $shortcut.WorkingDirectory = $InstallDir
    $shortcut.Description = "语音转文字工具"
    $shortcut.Save()
    Write-OK "桌面快捷方式已创建: $shortcutPath"
} else {
    Write-Warn "可执行文件不存在，跳过快捷方式创建"
    Write-Info "请先编译或下载 voice2text.exe"
}

# ============================================================
# 7. 编译（如果从源码安装）
# ============================================================
$sourceDir = Split-Path -Parent $PSScriptRoot
$sourceGoFile = Join-Path $sourceDir "main.go"
$compiledExe = Join-Path $sourceDir "voice2text.exe"

if ((Test-Path $sourceGoFile) -and $hasGo) {
    Write-Step "从源码编译..."
    Write-Info "检测到源码目录，正在编译..."
    
    Push-Location $sourceDir
    try {
        $env:CGO_ENABLED = "0"
        $env:GOOS = "windows"
        $env:GOARCH = "amd64"
        
        go build -ldflags="-H windowsgui" -o voice2text.exe . 2>&1 | ForEach-Object { Write-Info $_ }
        
        if ($LASTEXITCODE -eq 0 -and (Test-Path $compiledExe)) {
            Copy-Item $compiledExe "$InstallDir\voice2text.exe" -Force
            Write-OK "编译成功: $InstallDir\voice2text.exe"
        } else {
            Write-Fail "编译失败"
        }
    } finally {
        Pop-Location
    }
} else {
    Write-Info "跳过编译（未检测到源码或 Go 未安装）"
}

# ============================================================
# 8. 完成
# ============================================================
Write-Host "`n" -NoNewline
Write-Host "============================================" -ForegroundColor Green
Write-Host "   voice2text-go 安装完成!" -ForegroundColor Green
Write-Host "============================================" -ForegroundColor Green

Write-Host "`n安装位置: $InstallDir" -ForegroundColor White
Write-Host "配置文件: $configPath" -ForegroundColor White
Write-Host "日志文件: $env:TEMP\voice2text.log" -ForegroundColor White

Write-Host "`n硬件状态:" -ForegroundColor White
if ($hasNPU) {
    Write-Host "  NPU: $npuType [OK]" -ForegroundColor Green
} else {
    Write-Host "  NPU: 未检测到" -ForegroundColor Gray
}
if ($hasGPU) {
    Write-Host "  GPU: $gpuName [OK]" -ForegroundColor Green
} else {
    Write-Host "  GPU: 未检测到" -ForegroundColor Gray
}
Write-Host "  CPU: $cpuName" -ForegroundColor Gray

Write-Host "`n下一步:" -ForegroundColor Yellow
Write-Host "  1. 双击桌面快捷方式启动" -ForegroundColor White
Write-Host "  2. 右键托盘图标可切换快捷键" -ForegroundColor White
Write-Host "  3. 长按快捷键开始录音" -ForegroundColor White

Write-Host "`n如需帮助，请访问:" -ForegroundColor Gray
Write-Host "  https://github.com/lisiyu/voice2text-go" -ForegroundColor Cyan
