#Requires -RunAsAdministrator
<#
.SYNOPSIS
    voice2text-go 一键安装脚本
.DESCRIPTION
    自动检测系统硬件（NPU/CPU），从 GitHub Releases 下载主程序与依赖 DLL，
    模型文件从 HuggingFace 第三方下载，安装并配置语音转文字工具。
.NOTES
    在线安装（推荐，国内用 jsdelivr 镜像，raw.githubusercontent.com 常被重置）：
        irm https://cdn.jsdelivr.net/gh/lisiyu/voice2text-go@main/install.ps1 | iex
    或官方源：
        irm https://raw.githubusercontent.com/lisiyu/voice2text-go/main/install.ps1 | iex

    本地安装：
        本文件为 UTF-8 无 BOM 编码。若用 Windows PowerShell 5.1 直接执行，
        请用下面这条命令（显式按 UTF-8 读取），否则中文会被按系统 ANSI 代码页
        解析而乱码：
            powershell -NoProfile -Command "iex (Get-Content .\install.ps1 -Raw -Encoding UTF8)"
        或在 PowerShell 7+ 中直接执行 .\install.ps1

    自定义安装目录：
        irm https://cdn.jsdelivr.net/gh/lisiyu/voice2text-go@main/install.ps1 | iex -InstallDir "D:\Tools\voice2text-go"

    排查 NPU 问题时可强制走纯 CPU 组件集：
        ... | iex -ForceCPU
#>

param(
    [string]$InstallDir = "$env:USERPROFILE\voice2text-go",
    [switch]$SkipDownload,
    [switch]$Force,
    # 强制使用纯 CPU 组件集（即使检测到 NPU），用于排查 NPU 侧问题
    [switch]$ForceCPU,
    # 文件名 -> SHA256(hex)：提供后下载完成即校验，不提供则记警告跳过。
    # 键为 Release 资产名。例：.\install.ps1 -FileHashes @{"flexmlrt.dll"="ABC123..."}
    [hashtable]$FileHashes = @{}
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

# ============================================================
# 内置 SHA256 校验表（键为 Release 资产名）
# 更新依赖版本时，请同步更新本表与 Release 资产
# ============================================================
$BuiltinHashes = @{
    # --- NPU 组件集：Lemonade v1.8.4 ---
    "whisper-server-npu.exe" = "4026F7670FC13493AE72D8B5D69B64E59AA3C65EA1344D21D1DF1FD70CB658A9"
    "npu-ggml.dll"           = "863503801A2EE340C569FD77F22A69A5429FBA10C8BE872B09AE86779CF690D9"
    "npu-ggml-base.dll"      = "A9CFCAEA3F673343AAC77A998454D1DBFD55F4F97987A4CF3D354427DD2B8897"
    "npu-ggml-cpu.dll"       = "2C6AE362CA1441AEBE884F1F097A92B8AD8944179A4172988B26F0AE85104A25"
    "npu-whisper.dll"        = "981A10D321A129BBE3332A84CC2A94D0E9649D08FDD77F6F189E4D8D9144C543"
    "flexmlrt.dll"           = "FFB4637FA4FF7832FB6FF21C418536DAD122A345AC09C08FA4F07AD2C865A464"
    # --- CPU 组件集：whisper.cpp 上游 b5130 (whisper-bin-x64.zip) ---
    "whisper-server.exe"         = "74C13BCB83B944412B971886D37893611FB31846780C3A78DB3847B3132306BC"
    "cpu-ggml.dll"               = "C6E88687D6AA0238F2E834A87958F38D2DC96075A6A7BF6FB8B9CD728A0FAED2"
    "cpu-ggml-base.dll"          = "A5241B52206F61C9DFD6F0F53A8EF19076F9528EAD2DB1E039A66D7E03817BFC"
    "cpu-whisper.dll"            = "B7BB4BA92BD36B8AFE0C00059CA6A0F69C6767193BFB5F0761E9ED27B1087803"
    "cpu-ggml-cpu-x64.dll"       = "43CCF32B9B70AA4C47D0A12AC2ED3C241E0045571ACDAD565ECD9D1F99FFC08B"
    "cpu-ggml-cpu-sse42.dll"     = "740FC769EF433985DFDB24A609A42D5ACF188ABDA52287A4D89B89B567507C19"
    "cpu-ggml-cpu-sandybridge.dll" = "DE2AD5F84C7DFA557D515B3678D6452CE0216590268B2CA79CC537FE87CF239D"
    "cpu-ggml-cpu-haswell.dll"   = "6B772E094B8976E22B4C043BE86A1A8DEDA4B50511DC803A693B652C51B24944"
    "cpu-ggml-cpu-skylakex.dll"  = "9FA3F9D984CA42D568181DD345072299DB2978B328800343218C2C7F000B7152"
    "cpu-ggml-cpu-icelake.dll"   = "BBD87EA5920EDC401054848071AD2EF8C96BBF8007C4E4CD0EA82BA9B9E3BD10"
    "cpu-ggml-cpu-cascadelake.dll" = "DDF49BB749B34800AFCB3D6224544966A05C5D00F1D0B6565BEE9F3B010DD53C"
    "cpu-ggml-cpu-cannonlake.dll"  = "2C858781450B52EDA95C381232CC65C9E19CBF621CC7B254D8C44FDBAB77791E"
    "cpu-ggml-cpu-alderlake.dll"   = "5A5B11DCD38E321B13F85C95414940DB9EAB1132BE3DA6342F03DFB1D8E51BD5"
}

# 合并内置表与用户传入的覆盖项
$EffectiveHashes = $BuiltinHashes
if ($FileHashes -and $FileHashes.Count -gt 0) {
    foreach ($k in $FileHashes.Keys) { $EffectiveHashes[$k] = $FileHashes[$k] }
}

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

# 校验 SHA256。哈希表中无该文件时记警告并跳过。
function Test-FileHash {
    param([string]$Path, [string]$FileName, [string]$Desc)
    $expected = $EffectiveHashes[$FileName]
    if ([string]::IsNullOrWhiteSpace($expected)) {
        Write-Warn "$Desc 无内置 SHA256，跳过完整性校验"
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

# 校验字节数。大文件（模型）用尺寸兜底，防止下载到 HTML 错误页或截断文件。
function Test-FileSize {
    param([string]$Path, [int64]$ExpectedSize, [string]$Desc)
    if ($ExpectedSize -le 0) { return $true }
    $actual = (Get-Item $Path -ErrorAction Stop).Length
    if ($actual -eq $ExpectedSize) { return $true }
    Write-Fail "$Desc 尺寸不符: 期望 $ExpectedSize 字节, 实际 $actual 字节"
    return $false
}

# 确保目标文件就位：已存在则复用，否则从 $Urls 依次尝试下载。
# 返回 $true 表示文件可用。
function Get-RemoteFile {
    param(
        [string[]]$Urls,
        [string]$Dest,
        [string]$FileName,
        [string]$Desc,
        [int64]$ExpectedSize = 0,
        [int]$TimeoutSec = 900
    )

    if (-not $SkipDownload -and $Force -and (Test-Path $Dest)) {
        Remove-Item $Dest -Force -ErrorAction SilentlyContinue
    }

    if ((Test-Path $Dest) -and (Get-Item $Dest).Length -gt 0) {
        if (Test-FileSize -Path $Dest -ExpectedSize $ExpectedSize -Desc $Desc) {
            Write-OK "$Desc 已存在，跳过下载"
            return $true
        }
        Write-Warn "$Desc 本地文件尺寸异常，重新下载"
        Remove-Item $Dest -Force -ErrorAction SilentlyContinue
    }

    if ($SkipDownload) {
        Write-Warn "$Desc 缺失: $Dest"
        return $false
    }

    foreach ($url in $Urls) {
        if ([string]::IsNullOrWhiteSpace($url)) { continue }
        Write-Info "下载 $Desc ..."
        Write-Info "  URL: $url"
        $tmpFile = "$Dest.download"
        try {
            Invoke-WebRequest -Uri $url -OutFile $tmpFile -UseBasicParsing -TimeoutSec $TimeoutSec
            Move-Item -Path $tmpFile -Destination $Dest -Force

            if (-not (Test-FileSize -Path $Dest -ExpectedSize $ExpectedSize -Desc $Desc)) {
                throw "尺寸校验失败"
            }
            if (-not (Test-FileHash -Path $Dest -FileName $FileName -Desc $Desc)) {
                throw "SHA256 校验失败"
            }

            $mb = [math]::Round((Get-Item $Dest).Length / 1MB, 2)
            Write-OK "$Desc 下载完成 ($mb MB)"
            return $true
        } catch {
            Write-Fail "$Desc 下载失败: $_"
            if (Test-Path $tmpFile) { Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue }
            if (Test-Path $Dest) { Remove-Item $Dest -Force -ErrorAction SilentlyContinue }
        }
    }

    Write-Fail "$Desc 全部下载源均失败"
    return $false
}

# ============================================================
# GitHub Releases 配置
# ============================================================
$RepoOwner = "lisiyu"
$RepoName = "voice2text-go"
$LatestReleaseUrl = "https://api.github.com/repos/$RepoOwner/$RepoName/releases/latest"
$AppExeName = "voice2text.exe"
$releaseTag = ""

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
    Write-Info "Go 未安装（安装程序将直接下载预编译版本）"
}

$hasGit = Test-Command "git"
if ($hasGit) {
    Write-OK "Git"
} else {
    Write-Info "Git 未安装（不影响使用）"
}

# ============================================================
# 3. 创建目录结构
# ============================================================
Write-Step "创建安装目录..."

if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    Write-Info "创建: $InstallDir"
}
$binDir = Join-Path $InstallDir "bin"
if (-not (Test-Path $binDir)) {
    New-Item -ItemType Directory -Path $binDir -Force | Out-Null
    Write-Info "创建: $binDir"
}
Write-OK "目录结构就绪: $InstallDir"

# ============================================================
# 4. 解析 GitHub Release
# ============================================================
Write-Step "获取 GitHub Release 版本..."

if ($SkipDownload) {
    Write-Info "已指定 -SkipDownload，跳过 Release 查询"
} else {
    try {
        $release = Invoke-RestMethod -Uri $LatestReleaseUrl -Headers @{ "User-Agent" = "voice2text-installer" } -TimeoutSec 60
        $releaseTag = $release.tag_name
        Write-OK "最新版本: $releaseTag"
    } catch {
        Write-Fail "无法获取 GitHub Releases 信息: $_"
        Write-Warn "请手动从 ${RepoOwner}/${RepoName} 的 Releases 页面下载文件并放置到 ${binDir}:"
        Write-Info "https://github.com/$RepoOwner/$RepoName/releases/latest"
    }
}

# ============================================================
# 5. 下载 Whisper 运行组件（GitHub Release）
# ============================================================
Write-Step "下载 Whisper 运行组件..."

function New-ReleaseUrl {
    param([string]$Name)
    "https://github.com/$RepoOwner/$RepoName/releases/download/$releaseTag/$Name"
}

# ============================================================
# 依赖矩阵
# ------------------------------------------------------------
# NPU 与 CPU 是两套互不相同的构建（实测确认）：
#   * NPU 版来自 Lemonade v1.8.4，硬依赖 flexmlrt.dll 与 .rai 编码器；
#     若缺少 .rai，server 启动时会报 "failed to load Vitis AI model" 并退出。
#   * CPU 版来自 whisper.cpp 上游 b5130 (whisper-bin-x64.zip)，纯 CPU，
#     不需要 flexmlrt.dll 和 .rai。
# 两者同名的 ggml.dll / ggml-base.dll / whisper.dll 内容不同，
# 因此 Release 资产用 npu-/cpu- 前缀区分，下载后重命名为标准文件名。
# ============================================================

# NPU 组件：AssetName=Release 资产名，DestName=落盘文件名
$npuComponents = @(
    @{ Asset = "whisper-server-npu.exe"; Dest = "whisper-server-npu.exe"; Desc = "Whisper NPU Server (Lemonade v1.8.4)" }
    @{ Asset = "npu-ggml.dll"; Dest = "ggml.dll"; Desc = "GGML Core DLL (NPU 版)" }
    @{ Asset = "npu-ggml-base.dll"; Dest = "ggml-base.dll"; Desc = "GGML Base DLL (NPU 版)" }
    @{ Asset = "npu-ggml-cpu.dll"; Dest = "ggml-cpu.dll"; Desc = "GGML CPU Dispatch DLL (NPU 版)" }
    @{ Asset = "npu-whisper.dll"; Dest = "whisper.dll"; Desc = "Whisper DLL (NPU 版)" }
    @{ Asset = "flexmlrt.dll"; Dest = "flexmlrt.dll"; Desc = "FlexML Runtime (NPU)" }
)

# CPU 组件：上游按微架构提供多个 ggml-cpu 变体，全部随附以兼容任意 x64 CPU
$cpuComponents = @(
    @{ Asset = "whisper-server.exe"; Dest = "whisper-server.exe"; Desc = "Whisper Server (CPU, 上游 b5130)" }
    @{ Asset = "cpu-ggml.dll"; Dest = "ggml.dll"; Desc = "GGML Core DLL (CPU 版)" }
    @{ Asset = "cpu-ggml-base.dll"; Dest = "ggml-base.dll"; Desc = "GGML Base DLL (CPU 版)" }
    @{ Asset = "cpu-whisper.dll"; Dest = "whisper.dll"; Desc = "Whisper DLL (CPU 版)" }
    @{ Asset = "cpu-ggml-cpu-x64.dll"; Dest = "ggml-cpu-x64.dll"; Desc = "GGML CPU x64 baseline" }
    @{ Asset = "cpu-ggml-cpu-sse42.dll"; Dest = "ggml-cpu-sse42.dll"; Desc = "GGML CPU SSE4.2" }
    @{ Asset = "cpu-ggml-cpu-sandybridge.dll"; Dest = "ggml-cpu-sandybridge.dll"; Desc = "GGML CPU Sandy Bridge" }
    @{ Asset = "cpu-ggml-cpu-haswell.dll"; Dest = "ggml-cpu-haswell.dll"; Desc = "GGML CPU Haswell" }
    @{ Asset = "cpu-ggml-cpu-skylakex.dll"; Dest = "ggml-cpu-skylakex.dll"; Desc = "GGML CPU Skylake-X" }
    @{ Asset = "cpu-ggml-cpu-icelake.dll"; Dest = "ggml-cpu-icelake.dll"; Desc = "GGML CPU Ice Lake" }
    @{ Asset = "cpu-ggml-cpu-cascadelake.dll"; Dest = "ggml-cpu-cascadelake.dll"; Desc = "GGML CPU Cascade Lake" }
    @{ Asset = "cpu-ggml-cpu-cannonlake.dll"; Dest = "ggml-cpu-cannonlake.dll"; Desc = "GGML CPU Cannon Lake" }
    @{ Asset = "cpu-ggml-cpu-alderlake.dll"; Dest = "ggml-cpu-alderlake.dll"; Desc = "GGML CPU Alder Lake" }
)

# 非 NPU 机器可用 -ForceCPU 强制走 CPU 组件（便于排查问题）
$useNpuVariant = $hasNPU -and (-not $ForceCPU)
$components = if ($useNpuVariant) { $npuComponents } else { $cpuComponents }
$serverExeName = if ($useNpuVariant) { "whisper-server-npu.exe" } else { "whisper-server.exe" }

if ($hasNPU -and -not $useNpuVariant) {
    Write-Info "已指定 -ForceCPU，忽略检测到的 NPU，使用纯 CPU 组件"
}
if ($useNpuVariant) {
    Write-Info "使用 NPU 组件集 (Lemonade v1.8.4 + VitisAI 编码器)"
} else {
    Write-Info "使用纯 CPU 组件集 (whisper.cpp 上游 b5130)"
}

$readyFiles = @{}

foreach ($c in $components) {
    $dest = Join-Path $binDir $c.Dest
    if (Get-RemoteFile -Urls @((New-ReleaseUrl $c.Asset)) -Dest $dest -FileName $c.Asset -Desc $c.Desc -TimeoutSec 600) {
        $readyFiles[$c.Dest] = $true
    }
}

# ============================================================
# 6. 下载 Whisper 模型（HuggingFace 第三方托管）
# ============================================================
Write-Step "下载 Whisper 模型..."

# 尺寸为官方发布值，用于拦截截断/错误页下载
$modelFiles = @(
    @{
        Name = "ggml-large-v3-turbo.bin"
        Desc = "Whisper Large-v3-Turbo 解码器"
        SizeMB = "1,549MB"
        SizeBytes = 1624555275
        Urls = @(
            "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin",
            "https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin"
        )
    },
    @{
        Name = "ggml-large-v3-turbo-encoder-vitisai.rai"
        Desc = "Vitis AI NPU 编码器"
        SizeMB = "708MB"
        SizeBytes = 742511232
        OnlyNpu = $true
        Urls = @(
            "https://huggingface.co/amd/whisper-large-turbo-onnx-npu/resolve/main/ggml-large-v3-turbo-encoder-vitisai.rai",
            "https://hf-mirror.com/amd/whisper-large-turbo-onnx-npu/resolve/main/ggml-large-v3-turbo-encoder-vitisai.rai"
        )
    }
)

foreach ($f in $modelFiles) {
    if ($f.OnlyNpu -and -not $useNpuVariant) {
        Write-Info "跳过 $($f.Desc)（当前为纯 CPU 模式，放置 .rai 会导致 server 启动失败）"
        continue
    }
    $dest = Join-Path $binDir $f.Name
    $desc = "$($f.Desc) ($($f.SizeMB))"
    if (Get-RemoteFile -Urls $f.Urls -Dest $dest -FileName $f.Name -Desc $desc -ExpectedSize $f.SizeBytes -TimeoutSec 3600) {
        $readyFiles[$f.Name] = $true
    }
}

# ============================================================
# 7. 安装主程序 voice2text.exe
# ============================================================
Write-Step "安装主程序..."

$appExePath = Join-Path $InstallDir $AppExeName

# 7a. 若脚本位于源码目录且本机有 Go，则优先本地编译
$scriptRoot = $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($scriptRoot)) {
    $scriptRoot = ""
    Write-Info "脚本通过管道执行（无源码路径），将下载预编译版本"
}

$sourceGoFile = ""
if (-not [string]::IsNullOrWhiteSpace($scriptRoot)) {
    $candidate = Join-Path $scriptRoot "main.go"
    if (Test-Path $candidate) { $sourceGoFile = $candidate }
}

if ($sourceGoFile -and $hasGo -and -not $SkipDownload) {
    Write-Info "检测到源码目录，正在本地编译..."
    try {
        Push-Location $scriptRoot
        try {
            $env:CGO_ENABLED = "0"
            $env:GOOS = "windows"
            $env:GOARCH = "amd64"
            go build -ldflags="-H windowsgui" -o $appExePath . 2>&1 | ForEach-Object { Write-Info $_ }
            if ($LASTEXITCODE -eq 0 -and (Test-Path $appExePath)) {
                Write-OK "编译成功: $appExePath"
                $readyFiles[$AppExeName] = $true
            } else {
                Write-Warn "本地编译失败，改用预编译版本"
            }
        } finally {
            Pop-Location
        }
    } catch {
        Write-Warn "本地编译异常，改用预编译版本: $_"
    }
}

# 7b. 编译不可用时从 Release 下载
if (-not $readyFiles.ContainsKey($AppExeName)) {
    $exeUrls = @()
    if ($releaseTag) { $exeUrls += (New-ReleaseUrl $AppExeName) }
    if (Get-RemoteFile -Urls $exeUrls -Dest $appExePath -FileName $AppExeName -Desc "主程序 $AppExeName" -TimeoutSec 600) {
        $readyFiles[$AppExeName] = $true
    }
}

if (-not (Test-Path $appExePath)) {
    Write-Fail "主程序安装失败，后续步骤已跳过"
    Write-Host "`n请手动下载 $AppExeName 并放置到 $InstallDir" -ForegroundColor Yellow
    exit 1
}

# ============================================================
# 8. 生成配置文件（与主程序同目录，UTF-8 无 BOM）
# ============================================================
Write-Step "生成配置文件..."

$configPath = Join-Path $InstallDir "voice2text.json"

# 根据就位情况选择 whisper-server exe 和模型路径
$selectedExeName = ""
if ($readyFiles.ContainsKey("whisper-server-npu.exe")) {
    $selectedExeName = "whisper-server-npu.exe"
} elseif ($readyFiles.ContainsKey("whisper-server.exe")) {
    $selectedExeName = "whisper-server.exe"
}

$whisperServerExe = ""
$whisperModel = ""
if ($selectedExeName) {
    $whisperServerExe = (Join-Path $binDir $selectedExeName).Replace('\', '\\')
}
if ($readyFiles.ContainsKey("ggml-large-v3-turbo.bin")) {
    $whisperModel = (Join-Path $binDir "ggml-large-v3-turbo.bin").Replace('\', '\\')
}

$config = [ordered]@{
    key                 = "space"
    mod                 = ""
    hold_ms             = 300
    whisper_server_exe  = $whisperServerExe
    whisper_server_url  = "http://127.0.0.1:8080/inference"
    whisper_server_port = 8080
    whisper_model       = $whisperModel
    language            = "zh"
    prompt              = "以下是语音转写内容，使用简体中文，英文单词保持原文不要翻译，直接输出。"
    warn_recording_sec  = 90
    max_recording_sec   = 0
    split_long_audio    = $true
    auto_backend        = $true
} | ConvertTo-Json -Depth 10

if ((Test-Path $configPath) -and -not $Force) {
    Write-Warn "配置文件已存在，保留原设置: $configPath"
    Write-Info "如需覆盖请加 -Force 参数"
} else {
    # 必须无 BOM：Go 的 encoding/json 不接受 UTF-8 BOM
    $utf8NoBom = New-Object System.Text.UTF8Encoding $false
    [System.IO.File]::WriteAllText($configPath, $config, $utf8NoBom)
    Write-OK "配置文件已创建: $configPath"
    if ($whisperServerExe) {
        Write-Info "  whisper-server: $selectedExeName"
    } else {
        Write-Warn "  未找到 whisper-server，配置中该路径为空"
    }
    if ($whisperModel) {
        Write-Info "  模型: ggml-large-v3-turbo.bin"
    } else {
        Write-Warn "  未找到模型文件，配置中模型路径为空"
    }
}

# ============================================================
# 9. 创建桌面快捷方式
# ============================================================
Write-Step "创建快捷方式..."

$desktopPath = [Environment]::GetFolderPath("Desktop")
$shortcutPath = Join-Path $desktopPath "voice2text.lnk"

if (Test-Path $appExePath) {
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut($shortcutPath)
    $shortcut.TargetPath = $appExePath
    $shortcut.WorkingDirectory = $InstallDir
    $shortcut.Description = "语音转文字工具"
    $shortcut.Save()
    Write-OK "桌面快捷方式已创建: $shortcutPath"
} else {
    Write-Warn "主程序不存在，跳过快捷方式创建: $appExePath"
}

# ============================================================
# 10. 完成
# ============================================================
Write-Host "`n============================================" -ForegroundColor Green
Write-Host "   voice2text-go 安装完成!" -ForegroundColor Green
Write-Host "============================================" -ForegroundColor Green

Write-Host "`n安装位置: $InstallDir" -ForegroundColor White
Write-Host "主程序:   $appExePath" -ForegroundColor White
Write-Host "配置文件: $configPath" -ForegroundColor White
Write-Host "日志文件: $env:TEMP\voice2text.log" -ForegroundColor White

Write-Host "`n硬件状态:" -ForegroundColor White
if ($hasNPU) {
    Write-Host "  NPU: $npuType [OK]" -ForegroundColor Green
} else {
    Write-Host "  NPU: 未检测到（使用 CPU 推理）" -ForegroundColor Gray
}
if ($hasGPU) {
    Write-Host "  GPU: $gpuName [OK]" -ForegroundColor Green
} else {
    Write-Host "  GPU: 未检测到" -ForegroundColor Gray
}
Write-Host "  CPU: $cpuName" -ForegroundColor Gray

Write-Host "`n文件清单:" -ForegroundColor White
$summary = @(@{ Name = $AppExeName; Desc = "主程序" })
$summary += $components | ForEach-Object { @{ Name = $_.Dest; Desc = $_.Desc } }
$summary += @($modelFiles | Where-Object { -not $_.OnlyNpu -or $useNpuVariant } | ForEach-Object { @{ Name = $_.Name; Desc = $_.Desc } })

$failCount = 0
foreach ($f in $summary) {
    $present = if ($f.Name -eq $AppExeName) { Test-Path $appExePath } else { $readyFiles.ContainsKey($f.Name) }
    if ($present) {
        Write-Host "  [OK] $($f.Desc)" -ForegroundColor Green
    } else {
        Write-Host "  [X] $($f.Desc)  <缺失>" -ForegroundColor Red
        $failCount++
    }
}

Write-Host "`n使用方式:" -ForegroundColor Yellow
Write-Host "  1. 双击桌面快捷方式启动（或运行 $appExePath）" -ForegroundColor White
Write-Host "  2. 右键托盘图标可切换快捷键与查看日志" -ForegroundColor White
Write-Host "  3. 长按快捷键开始录音，松开后自动转写并复制" -ForegroundColor White

if ($failCount -gt 0) {
    Write-Host "`n[!] $failCount 个文件缺失，请重新运行本脚本，或手动放置到: $binDir" -ForegroundColor Yellow
}

Write-Host "`n帮助文档: https://github.com/$RepoOwner/$RepoName" -ForegroundColor Cyan
