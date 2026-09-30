#Requires -RunAsAdministrator
<#
.SYNOPSIS
    voice2text-go 一键安装脚本
.DESCRIPTION
    自动检测系统硬件（NPU/GPU/CPU），从 GitHub Releases 下载依赖，模型文件从第三方下载，安装并配置语音转文字工具。
.NOTES
    以管理员权限运行：右键 install.ps1 -> 使用 PowerShell 运行
#>

param(
    [string]$InstallDir = "$env:USERPROFILE\voice2text-go",
    [switch]$SkipDownload,
    [switch]$Force,
    # 文件名 -> SHA256(hex)：提供后下载完成即校验，不提供则记警告跳过。
    # 例：.\install.ps1 -FileHashes @{"whisper-server.exe"="ABC123..."; "ggml-large-v3-turbo.bin"="DEF456..."}
    [hashtable]$FileHashes = @{}
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

# Test-FileHash: 校验下载文件的 SHA256。$FileHashes 未提供该文件哈希时记警告并跳过。
function Test-FileHash {
    param([string]$Path, [string]$FileName, [string]$Desc)
    $expected = $FileHashes[$FileName]
    if ([string]::IsNullOrWhiteSpace($expected)) {
        Write-Warn "$Desc 未提供 SHA256 校验和，跳过完整性校验"
        return $true
    }
    try {
        $actual = (Get-FileHash -Path $Path -Algorithm SHA256 -ErrorAction Stop).Hash
    } catch {
        Write-Fail "$Desc 计算 SHA256 失败: $_"
        return $false
    }
    if ($actual -ieq $expected.Trim()) {
        Write-OK "$Desc SHA256 校验通过"
        return $true
    }
    Write-Fail "$Desc SHA256 校验失败: 期望 $expected, 实际 $actual"
    return $false
}

# ============================================================
# GitHub Releases 配置（所有依赖文件从这里下载）
# ============================================================
$RepoOwner = "lisiyu"
$RepoName = "voice2text-go"
$LatestReleaseUrl = "https://api.github.com/repos/$RepoOwner/$RepoName/releases/latest"

# ============================================================
# 模型文件下载地址（第三方托管，大文件不适合放 GitHub）
# ============================================================
$ModelBaseUrl = "" # TODO: 替换为实际下载地址前缀，例如 https://your-cdn.example/models/
# 完整 URL：
#   $ModelBaseUrl + "ggml-large-v3-turbo.bin"              (~1.5GB)
#   $ModelBaseUrl + "ggml-large-v3-turbo-encoder-vitisai.rai" (~708MB)

# ============================================================
# 1. 硬件检测
# ============================================================
Write-Step "检测系统硬件..."

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

$hasGo = Test-Command "go"
if ($hasGo) {
    $goVersion = (go version 2>$null) -replace ".*go(\d+\.\d+\.\d+).*", '$1'
    Write-OK "Go $goVersion"
} else {
    Write-Warn "Go 未安装，将使用预编译二进制"
}

$hasGit = Test-Command "git"
if ($hasGit) {
    Write-OK "Git"
} else {
    Write-Warn "Git 未安装，部分功能受限"
}

# ============================================================
# 3. 创建目录结构（仅 bin/）
# ============================================================
Write-Step "创建安装目录..."

$binDir = "$InstallDir\bin"
if (-not (Test-Path $binDir)) {
    New-Item -ItemType Directory -Path $binDir -Force | Out-Null
    Write-Info "创建: $binDir"
}
Write-OK "目录结构就绪"

# 本地已有文件检测：用户机器上可能已有 lemonade 等工具自带的 whisper 组件
# （exe / 模型），优先复用，不重复下载。按优先级搜索，$binDir 最优先。
$localSearchDirs = @(
    $binDir,
    "$env:USERPROFILE\.cache\lemonade\bin\whispercpp\npu",
    "$env:USERPROFILE\.cache\lemonade\bin\whispercpp\cpu",
    "$env:USERPROFILE\models\lemonade\whispercpp"
)

# Find-LocalFile 在候选目录中查找已存在且非空的文件，返回完整路径；找不到返回 ""
function Find-LocalFile {
    param([string]$FileName)
    foreach ($d in $localSearchDirs) {
        if ([string]::IsNullOrWhiteSpace($d)) { continue }
        $p = Join-Path $d $FileName
        if ((Test-Path $p) -and ((Get-Item $p).Length -gt 0)) {
            return $p
        }
    }
    return ""
}

# $resolvedPaths 记录每个逻辑组件实际解析到的路径（下载到 bin\ 或复用本地已有），
# 生成配置文件时使用，保证配置指向真实存在的文件。
$resolvedPaths = @{}

# ============================================================
# 4. 从 GitHub Releases 下载依赖文件
# ============================================================
Write-Step "下载 Whisper 组件..."

# 通用 DLL（所有硬件都需要，总计 ~8MB）
$commonDeps = @(
    @{ Name = "ggml.dll"; Desc = "GGML Core DLL" },
    @{ Name = "ggml-base.dll"; Desc = "GGML Base DLL" },
    @{ Name = "ggml-cpu.dll"; Desc = "GGML CPU DLL" },
    @{ Name = "whisper.dll"; Desc = "Whisper DLL" }
)

# NPU 专用依赖（仅 NPU 机器下载）
$npuDeps = @(
    @{ Name = "flexmlrt.dll"; Desc = "FlexML Runtime (NPU)" }
)

# 模型文件（第三方下载）
$modelFiles = @(
    @{ Name = "ggml-large-v3-turbo.bin"; Desc = "Whisper Large-v3-Turbo 模型 (解码器)"; SizeMB = "~1,549" },
    @{ Name = "ggml-large-v3-turbo-encoder-vitisai.rai"; Desc = "Vitis AI NPU 编码器"; SizeMB = "~708"; OnlyNpu = $true }
)

$downloadedFiles = @{}

if (-not $SkipDownload) {
    try {
        Write-Info "获取 GitHub Releases 最新标签..."
        $release = Invoke-RestMethod -Uri $LatestReleaseUrl -Headers @{ "User-Agent"="voice2text-installer" }
        $tag = $release.tag_name
        Write-OK "最新版本: $tag"

        # --- 通用 DLL：优先复用本机已有（exe 同目录的 DLL 才能被加载到） ---
        foreach ($f in $commonDeps) {
            $foundDll = Find-LocalFile $f.Name
            if ($foundDll -ne "") {
                Write-OK "$($f.Desc) 复用本地已有: $foundDll"
                $downloadedFiles[$f.Name] = $true
                continue
            }
            $localPath = Join-Path $binDir $f.Name

            $assetUrl = "https://github.com/$RepoOwner/$RepoName/releases/download/$tag/$($f.Name)"
            Write-Info "下载 $($f.Desc) ..."

            try {
                $tmpFile = "$localPath.download"
                Invoke-WebRequest -Uri $assetUrl -OutFile $tmpFile -UseBasicParsing -TimeoutSec 300
                Move-Item -Path $tmpFile -Destination $localPath -Force
                if (-not (Test-FileHash -Path $localPath -FileName $f.Name -Desc $f.Desc)) {
                    Remove-Item $localPath -Force -ErrorAction SilentlyContinue
                    throw "$($f.Desc) SHA256 校验失败，已删除不可信文件"
                }
                Write-OK "$($f.Desc) 下载完成 ($(('{0:N2}' -f ((Get-Item $localPath).Length / 1MB))) MB)"
                $downloadedFiles[$f.Name] = $true
            } catch {
                Write-Fail "$($f.Desc) 下载失败: $_"
                if (Test-Path $tmpFile) { Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue }
            }
        }

        # --- 下载 NPU 专用依赖（仅 NPU 机器）---
        if ($hasNPU) {
            foreach ($f in $npuDeps) {
                $foundDll = Find-LocalFile $f.Name
                if ($foundDll -ne "") {
                    Write-OK "$($f.Desc) 复用本地已有: $foundDll"
                    $downloadedFiles[$f.Name] = $true
                    continue
                }
                $localPath = Join-Path $binDir $f.Name

                $assetUrl = "https://github.com/$RepoOwner/$RepoName/releases/download/$tag/$($f.Name)"
                Write-Info "下载 $($f.Desc) ..."

                try {
                    $tmpFile = "$localPath.download"
                    Invoke-WebRequest -Uri $assetUrl -OutFile $tmpFile -UseBasicParsing -TimeoutSec 300
                    Move-Item -Path $tmpFile -Destination $localPath -Force
                    if (-not (Test-FileHash -Path $localPath -FileName $f.Name -Desc $f.Desc)) {
                        Remove-Item $localPath -Force -ErrorAction SilentlyContinue
                        throw "$($f.Desc) SHA256 校验失败，已删除不可信文件"
                    }
                    Write-OK "$($f.Desc) 下载完成 ($(('{0:N2}' -f ((Get-Item $localPath).Length / 1MB))) MB)"
                    $downloadedFiles[$f.Name] = $true
                } catch {
                    Write-Fail "$($f.Desc) 下载失败: $_"
                    if (Test-Path $tmpFile) { Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue }
                }
            }

            # --- whisper-server NPU 版：优先复用本机已有（bin\ / lemonade 等），找不到再下载 ---
            $foundExe = Find-LocalFile "whisper-server-npu.exe"
            if ($foundExe -eq "") { $foundExe = Find-LocalFile "whisper-server.exe" }
            if ($foundExe -ne "") {
                Write-OK "Whisper NPU Server 复用本地已有: $foundExe"
                $downloadedFiles["whisper-server-npu.exe"] = $true
                $resolvedPaths["whisper-server"] = $foundExe
            } else {
                $whisperLocalPath = Join-Path $binDir "whisper-server-npu.exe"
                $assetUrl = "https://github.com/$RepoOwner/$RepoName/releases/download/$tag/whisper-server-npu.exe"
                Write-Info "下载 Whisper NPU Server ..."

                try {
                    $tmpFile = "$whisperLocalPath.download"
                    Invoke-WebRequest -Uri $assetUrl -OutFile $tmpFile -UseBasicParsing -TimeoutSec 300
                    Move-Item -Path $tmpFile -Destination $whisperLocalPath -Force
                    if (-not (Test-FileHash -Path $whisperLocalPath -FileName "whisper-server-npu.exe" -Desc "Whisper NPU Server")) {
                        Remove-Item $whisperLocalPath -Force -ErrorAction SilentlyContinue
                        throw "Whisper NPU Server SHA256 校验失败，已删除不可信文件"
                    }
                    Write-OK "Whisper NPU Server 下载完成 ($(('{0:N2}' -f ((Get-Item $whisperLocalPath).Length / 1MB))) MB)"
                    $downloadedFiles["whisper-server-npu.exe"] = $true
                    $resolvedPaths["whisper-server"] = $whisperLocalPath
                } catch {
                    Write-Fail "Whisper NPU Server 下载失败: $_"
                    if (Test-Path $tmpFile) { Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue }
                }
            }

        } else {
            # --- 非 NPU 机器：通用 whisper-server.exe，优先复用本机已有 ---
            $foundExe = Find-LocalFile "whisper-server.exe"
            if ($foundExe -eq "") { $foundExe = Find-LocalFile "whisper-server-cpu.exe" }
            if ($foundExe -ne "") {
                Write-OK "Whisper Server 复用本地已有: $foundExe"
                $downloadedFiles["whisper-server.exe"] = $true
                $resolvedPaths["whisper-server"] = $foundExe
            } else {
                $whisperLocalPath = Join-Path $binDir "whisper-server.exe"
                $assetUrl = "https://github.com/$RepoOwner/$RepoName/releases/download/$tag/whisper-server.exe"
                Write-Info "下载 Whisper Server (通用/CPU) ..."

                try {
                    $tmpFile = "$whisperLocalPath.download"
                    Invoke-WebRequest -Uri $assetUrl -OutFile $tmpFile -UseBasicParsing -TimeoutSec 300
                    Move-Item -Path $tmpFile -Destination $whisperLocalPath -Force
                    if (-not (Test-FileHash -Path $whisperLocalPath -FileName "whisper-server.exe" -Desc "Whisper Server")) {
                        Remove-Item $whisperLocalPath -Force -ErrorAction SilentlyContinue
                        throw "Whisper Server SHA256 校验失败，已删除不可信文件"
                    }
                    Write-OK "Whisper Server (通用/CPU) 下载完成 ($(('{0:N2}' -f ((Get-Item $whisperLocalPath).Length / 1MB))) MB)"
                    $downloadedFiles["whisper-server.exe"] = $true
                    $resolvedPaths["whisper-server"] = $whisperLocalPath
                } catch {
                    Write-Fail "Whisper Server 下载失败: $_"
                    if (Test-Path $tmpFile) { Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue }
                }
            }
        }

    } catch {
        Write-Fail "无法获取 GitHub Releases 信息: $_"
        Write-Warn "请手动从以下地址下载并放置到 ${binDir}:"
        Write-Info "https://github.com/$RepoOwner/$RepoName/releases/latest"
    }
} else {
    Write-Info "跳过下载，检查本地文件..."
    foreach ($f in $commonDeps) {
        $foundDll = Find-LocalFile $f.Name
        if ($foundDll -ne "") {
            Write-OK "$($f.Desc) 已存在: $foundDll"
            $downloadedFiles[$f.Name] = $true
        } else {
            Write-Warn "$($f.Desc) 缺失（已搜索 bin\ 及常见 lemonade 目录）"
        }
    }
    if ($hasNPU) {
        foreach ($f in $npuDeps) {
            $foundDll = Find-LocalFile $f.Name
            if ($foundDll -ne "") {
                Write-OK "$($f.Desc) 已存在: $foundDll"
                $downloadedFiles[$f.Name] = $true
            } else {
                Write-Warn "$($f.Desc) 缺失（已搜索 bin\ 及常见 lemonade 目录）"
            }
        }
        $foundExe = Find-LocalFile "whisper-server-npu.exe"
        if ($foundExe -eq "") { $foundExe = Find-LocalFile "whisper-server.exe" }
        if ($foundExe -ne "") {
            Write-OK "Whisper NPU Server 已存在: $foundExe"
            $downloadedFiles["whisper-server-npu.exe"] = $true
            $resolvedPaths["whisper-server"] = $foundExe
        } else {
            Write-Warn "Whisper NPU Server 缺失（已搜索 bin\ 及常见 lemonade 目录）"
        }
    } else {
        $foundExe = Find-LocalFile "whisper-server.exe"
        if ($foundExe -eq "") { $foundExe = Find-LocalFile "whisper-server-cpu.exe" }
        if ($foundExe -ne "") {
            Write-OK "Whisper Server 已存在: $foundExe"
            $downloadedFiles["whisper-server.exe"] = $true
            $resolvedPaths["whisper-server"] = $foundExe
        } else {
            Write-Warn "Whisper Server 缺失（已搜索 bin\ 及常见 lemonade 目录）"
        }
    }
}

# ============================================================
# 5. 从第三方下载模型文件
# ============================================================
Write-Step "下载 Whisper 模型..."

if ($ModelBaseUrl -eq "") {
    Write-Info "模型下载地址未配置（`$ModelBaseUrl 为空），将只检测本机已有模型"
}
foreach ($f in $modelFiles) {
    # NPU 编码器仅 NPU 机器需要
    if ($f.OnlyNpu -and -not $hasNPU) {
        Write-Info "跳过 $($f.Desc)（非 NPU 硬件不需要）"
        continue
    }

    # 本机已有模型优先复用（bin\ / lemonade 等常见位置）
    $foundModel = Find-LocalFile $f.Name
    if ($foundModel -ne "") {
        Write-OK "$($f.Desc) ($($f.SizeMB)) 复用本地已有: $foundModel"
        $downloadedFiles[$f.Name] = $true
        $resolvedPaths[$f.Name] = $foundModel
        continue
    }

    if ($SkipDownload) {
        Write-Warn "$($f.Desc) ($($f.SizeMB)) 缺失: $(Join-Path $binDir $f.Name)"
        continue
    }

    if ($ModelBaseUrl -eq "") {
        Write-Warn "$($f.Desc) ($($f.SizeMB)) 未找到本地文件，请手动放置到: $(Join-Path $binDir $f.Name)"
        continue
    }

    $localPath = Join-Path $binDir $f.Name
    $modelUrl = "$ModelBaseUrl$($f.Name)"
    Write-Info "下载 $($f.Desc) ($($f.SizeMB)) ..."
    Write-Info "  URL: $modelUrl"

    try {
        $tmpFile = "$localPath.download"
        Invoke-WebRequest -Uri $modelUrl -OutFile $tmpFile -UseBasicParsing -TimeoutSec 600
        Move-Item -Path $tmpFile -Destination $localPath -Force
        if (-not (Test-FileHash -Path $localPath -FileName $f.Name -Desc $f.Desc)) {
            Remove-Item $localPath -Force -ErrorAction SilentlyContinue
            throw "$($f.Desc) SHA256 校验失败，已删除不可信文件"
        }
        Write-OK "$($f.Desc) ($($f.SizeMB)) 下载完成"
        $downloadedFiles[$f.Name] = $true
        $resolvedPaths[$f.Name] = $localPath
    } catch {
        Write-Fail "$($f.Desc) 下载失败: $_"
        if (Test-Path $tmpFile) { Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue }
    }
}

# ============================================================
# 6. 生成配置文件
# ============================================================
Write-Step "生成配置文件..."

$configPath = "$InstallDir\voice2text.json"

# 根据实际解析到的文件位置（下载到 bin\ 或复用本机已有）生成配置
# 使用实际解析到的路径（下载到 bin\ 或复用本机已有），而不是硬拼 bin\
$whisperServerExe = ""
if ($resolvedPaths.ContainsKey("whisper-server")) {
    $whisperServerExe = $resolvedPaths["whisper-server"].Replace('\', '\\')
}
$whisperModel = ""
if ($resolvedPaths.ContainsKey("ggml-large-v3-turbo.bin")) {
    $whisperModel = $resolvedPaths["ggml-large-v3-turbo.bin"].Replace('\', '\\')
}
# whisper-cli 作为 CPU 兜底：检测本机已有（lemonade 等），找不到则留空
$whisperCli = Find-LocalFile "whisper-cli.exe"
if ($whisperCli -ne "") {
    Write-OK "Whisper CLI 复用本地已有: $whisperCli"
}

$config = @{
    key = "space"
    mod = ""
    hold_ms = 300
    whisper_server_exe = $whisperServerExe
    whisper_server_url = "http://127.0.0.1:8080/inference"
    whisper_server_port = 8080
    whisper_model = $whisperModel
    language = "zh"
    prompt = "以下是语音转写内容，使用简体中文，英文单词保持原文不要翻译，直接输出。"
    warn_recording_sec = 90
    max_recording_sec = 0
    split_long_audio = $true
    auto_backend = $true
}
if ($whisperCli -ne "") {
    $config["whisper_cli"] = $whisperCli
}
$config | ConvertTo-Json -Depth 10

if ((Test-Path $configPath) -and -not $Force) {
    Write-Warn "配置文件已存在: $configPath"
    Write-Info "使用 -Force 参数覆盖现有配置"
} else {
    $config | Out-File -FilePath $configPath -Encoding UTF8
    Write-OK "配置文件已创建: $configPath"
}

# ============================================================
# 7. 创建快捷方式
# ============================================================
Write-Step "创建快捷方式..."

$desktopPath = [Environment]::GetFolderPath("Desktop")
$shortcutPath = "$desktopPath\voice2text.lnk"
$exeFullPath = if (Test-Path "$InstallDir\voice2text.exe") { "$InstallDir\voice2text.exe" } else { Write-Warn "未找到 voice2text.exe，请先编译或下载主程序" }

if ($exeFullPath) {
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut($shortcutPath)
    $shortcut.TargetPath = $exeFullPath
    $shortcut.WorkingDirectory = $InstallDir
    $shortcut.Description = "语音转文字工具"
    $shortcut.Save()
    Write-OK "桌面快捷方式已创建: $shortcutPath"
}

# ============================================================
# 8. 编译（如果从源码安装）
# ============================================================
# 注意：通过 irm ... | iex 管道执行时 $PSScriptRoot 为空，此时跳过源码编译
$sourceDir = if ($PSScriptRoot) { Split-Path -Parent $PSScriptRoot } else { "" }
$sourceGoFile = if ($sourceDir -ne "") { Join-Path $sourceDir "main.go" } else { "" }
$compiledExe = if ($sourceDir -ne "") { Join-Path $sourceDir "voice2text.exe" } else { "" }

if (($sourceGoFile -ne "") -and (Test-Path $sourceGoFile) -and $hasGo) {
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
# 9. 完成
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

Write-Host "`n下载摘要:" -ForegroundColor White
$allFiles = @($commonDeps) + @($npuDeps | Where-Object { $hasNPU }) + @(,@{ Name = if ($hasNPU) { "whisper-server-npu.exe" } else { "whisper-server.exe" }; Desc = if ($hasNPU) { "Whisper NPU Server (专用)" } else { "Whisper Server (通用/CPU)" }}) + @($modelFiles | Where-Object { -not $_.OnlyNpu -or $hasNPU })
$successCount = 0
$failCount = 0
foreach ($f in $allFiles) {
    if ($downloadedFiles.ContainsKey($f.Name)) {
        Write-Host "  [OK] $($f.Desc)" -ForegroundColor Green
        $successCount++
    } else {
        Write-Host "  [X] $($f.Desc)" -ForegroundColor Red
        $failCount++
    }
}

Write-Host "`n下一步:" -ForegroundColor Yellow
Write-Host "  1. 双击桌面快捷方式启动（或运行 $InstallDir\voice2text.exe）" -ForegroundColor White
Write-Host "  2. 右键托盘图标可切换快捷键" -ForegroundColor White
Write-Host "  3. 长按快捷键开始录音" -ForegroundColor White

if ($failCount -gt 0) {
    Write-Host "`n[!] $failCount 个文件下载失败，请检查日志或手动放置到: $binDir" -ForegroundColor Yellow
}

Write-Host "`n如需帮助，请访问:" -ForegroundColor Gray
Write-Host "  https://github.com/$RepoOwner/$RepoName" -ForegroundColor Cyan
