# deepx one-click installer (Windows / PowerShell)
#
# Usage:
#   irm https://raw.githubusercontent.com/itmisx/deepx-code/main/scripts/install.ps1 | iex
#
# 直接运行(克隆仓库后):
#   .\scripts\install.ps1
#   .\scripts\install.ps1 -Version v0.1.0
#   .\scripts\install.ps1 -Prefix "C:\Tools\deepx"
#   .\scripts\install.ps1 -FromSource
#
# 环境变量覆盖(`irm | iex` 模式下无法传参,只能用 env):
#   $env:DEEPX_VERSION="v0.1.0"; irm .../install.ps1 | iex
#   $env:DEEPX_PREFIX="C:\Tools";  irm .../install.ps1 | iex
#   $env:DEEPX_FROM_SOURCE="1";   irm .../install.ps1 | iex

[CmdletBinding()]
param(
    [string]$Version    = $env:DEEPX_VERSION,
    [string]$Prefix     = $env:DEEPX_PREFIX,
    [switch]$FromSource = [bool]$env:DEEPX_FROM_SOURCE
)

$ErrorActionPreference = 'Stop'

# ---------------------------------------------------------------------------
# 固定配置
# ---------------------------------------------------------------------------
$Owner   = 'itmisx'
$Repo    = 'deepx-code'
$BinName = 'deepx.exe'

if (-not $Prefix) { $Prefix = Join-Path $env:LOCALAPPDATA 'Programs\deepx' }

# ---------------------------------------------------------------------------
# 日志
# ---------------------------------------------------------------------------
function Write-Step    ($msg) { Write-Host "`n==> $msg" -ForegroundColor Blue }
function Write-Info    ($msg) { Write-Host "[INFO]  $msg" -ForegroundColor Cyan }
function Write-Ok      ($msg) { Write-Host "[OK]    $msg" -ForegroundColor Green }
function Write-WarnMsg ($msg) { Write-Host "[WARN]  $msg" -ForegroundColor Yellow }
function Write-Err     ($msg) { Write-Host "[ERROR] $msg" -ForegroundColor Red }

# ---------------------------------------------------------------------------
# Banner
# ---------------------------------------------------------------------------
Write-Host ""
Write-Host "  +-----------------+" -ForegroundColor Cyan
Write-Host "  |     deepx       |   终端里的 AI 代码助手" -ForegroundColor Cyan
Write-Host "  +-----------------+   github.com/$Owner/$Repo" -ForegroundColor Cyan
Write-Host ""

# ---------------------------------------------------------------------------
# Step 1: 检测架构
# ---------------------------------------------------------------------------
Write-Step "Detecting platform"

$archRaw = $env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) { $archRaw = $env:PROCESSOR_ARCHITEW6432 }

switch ($archRaw) {
    'AMD64' { $Arch = 'amd64' }
    'ARM64' { $Arch = 'arm64' }
    default {
        Write-Err "Unsupported architecture: $archRaw"
        exit 1
    }
}

if ($Arch -eq 'arm64' -and -not $FromSource) {
    Write-Err "Windows arm64 暂未发布预编译二进制。请加 -FromSource 自行编译,或装 amd64 包走 x64 模拟。"
    exit 1
}

Write-Ok "Platform: windows/$Arch"

$BinDir = $Prefix
$BinPath = Join-Path $BinDir $BinName

# ---------------------------------------------------------------------------
# Step 2: 工具检查 + 从源码分支
# ---------------------------------------------------------------------------
if ($FromSource) {
    Write-Step "Building from source"
    foreach ($cmd in @('git', 'go')) {
        if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
            Write-Err "需要 ``$cmd``,请先安装。go: https://go.dev/dl/  git: https://git-scm.com/download/win"
            exit 1
        }
    }
    $goVer = (& go version) -replace '^go version go([\d.]+).*', '$1'
    Write-Info "Found Go $goVer"

    $tmp = New-Item -ItemType Directory -Path (Join-Path $env:TEMP "deepx-build-$([guid]::NewGuid().ToString('N'))") -Force
    try {
        Write-Info "Cloning $Owner/$Repo into $tmp..."
        & git clone --depth=1 "https://github.com/$Owner/$Repo.git" (Join-Path $tmp $Repo) | Out-Null
        Push-Location (Join-Path $tmp $Repo)
        try {
            Write-Info "Running go build..."
            $builtinVer = Get-Date -Format 'yyyyMMddHHmmss'  # 注入内嵌 skill 版本号,触发安装后自动刷新
            & go build -trimpath -ldflags="-s -w -X deepx/skill.builtinVersion=$builtinVer" -o $BinName .
            if ($LASTEXITCODE -ne 0) { throw "go build failed" }
            New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
            Copy-Item -Force (Join-Path (Get-Location) $BinName) $BinPath
            Write-Ok "Built and installed: $BinPath"
        } finally {
            Pop-Location
        }
    } finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
}
else {
    # ---------------------------------------------------------------------------
    # Step 3: 解析版本
    # ---------------------------------------------------------------------------
    Write-Step "Resolving version"
    if (-not $Version) {
        Write-Info "Querying latest release from GitHub..."
        try {
            $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$Owner/$Repo/releases/latest" `
                                      -Headers @{ 'Accept' = 'application/vnd.github+json' } `
                                      -UseBasicParsing
            $Version = $rel.tag_name
        } catch {
            Write-Err "无法解析最新版本: $($_.Exception.Message)"
            Write-Info "请显式指定: -Version v0.1.0  或  `$env:DEEPX_VERSION='v0.1.0'"
            exit 1
        }
    }
    if (-not $Version) {
        Write-Err "未能确定版本号"
        exit 1
    }
    Write-Ok "Version: $Version"

    # ---------------------------------------------------------------------------
    # Step 4: 下载 + 校验
    # ---------------------------------------------------------------------------
    Write-Step "Downloading release asset"

    # goreleaser v2 的 {{.Version}} 不含 v 前缀,产物名 e.g. deepx_0.1.0_windows_amd64.zip
    # URL 路径里的 tag 仍保留 v 前缀。
    # 注意:asset 前缀来自 goreleaser 的 project_name(deepx),不是 GitHub 仓库名(deepx-code)。
    $versionNoV = $Version -replace '^v', ''
    $assetPrefix = $BinName -replace '\.exe$', ''
    $asset = "${assetPrefix}_${versionNoV}_windows_${Arch}.zip"
    $url   = "https://github.com/$Owner/$Repo/releases/download/$Version/$asset"
    $tmp   = New-Item -ItemType Directory -Path (Join-Path $env:TEMP "deepx-install-$([guid]::NewGuid().ToString('N'))") -Force
    $zipPath = Join-Path $tmp $asset
    $sumsPath = Join-Path $tmp 'checksums.txt'

    try {
        Write-Info "URL: $url"
        try {
            Invoke-WebRequest -Uri $url -OutFile $zipPath -UseBasicParsing
        } catch {
            Write-Err "下载失败: $($_.Exception.Message)"
            Write-Info "在浏览器查看可用资产: https://github.com/$Owner/$Repo/releases/tag/$Version"
            exit 1
        }
        Write-Ok "Downloaded: $([math]::Round((Get-Item $zipPath).Length / 1MB, 2)) MB"

        # 校验 SHA256(可选,无则跳过)
        try {
            Invoke-WebRequest -Uri "https://github.com/$Owner/$Repo/releases/download/$Version/checksums.txt" `
                              -OutFile $sumsPath -UseBasicParsing -ErrorAction Stop
            $expected = (Get-Content $sumsPath | Select-String -SimpleMatch " $asset" | Select-Object -First 1).Line `
                            -split '\s+' | Select-Object -First 1
            if ($expected) {
                Write-Info "Verifying checksum..."
                $actual = (Get-FileHash $zipPath -Algorithm SHA256).Hash.ToLower()
                if ($actual -ne $expected.ToLower()) {
                    Write-Err "校验失败: 期望 $expected, 实际 $actual"
                    exit 1
                }
                Write-Ok "Checksum OK"
            } else {
                Write-WarnMsg "checksums.txt 里没找到 $asset,跳过校验"
            }
        } catch {
            Write-WarnMsg "未拉到 checksums.txt,跳过校验"
        }

        # ---------------------------------------------------------------------------
        # Step 5: 解压 + 安装
        # ---------------------------------------------------------------------------
        Write-Step "Installing to $BinDir"

        $extract = Join-Path $tmp 'extracted'
        Expand-Archive -Path $zipPath -DestinationPath $extract -Force
        $binSrc = Get-ChildItem -Path $extract -Recurse -Filter $BinName -File | Select-Object -First 1
        if (-not $binSrc) {
            Write-Err "解压后未找到 $BinName"
            exit 1
        }

        New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
        if (Test-Path $BinPath) {
            Write-Info "备份现有 $BinName 到 ${BinName}.bak"
            Move-Item -Force $BinPath "$BinPath.bak"
        }
        Copy-Item -Force $binSrc.FullName $BinPath
        Write-Ok "Installed: $BinPath"
    } finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
}

# ---------------------------------------------------------------------------
# Step 6: 持久化 PATH(User 级,不污染系统)
# ---------------------------------------------------------------------------
Write-Step "Setting up PATH"

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$userPathDirs = if ($userPath) { $userPath -split ';' | Where-Object { $_ } } else { @() }

if ($userPathDirs -contains $BinDir) {
    Write-Ok "$BinDir 已在 User PATH"
} else {
    $newPath = (@($userPathDirs) + $BinDir) -join ';'
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Write-Ok "已加入 User PATH(新开终端生效)"
    # 当前 session 也立即可用
    $env:Path = "$env:Path;$BinDir"
}

# ---------------------------------------------------------------------------
# Step 7: 验证
# ---------------------------------------------------------------------------
Write-Step "Verifying"

if (Test-Path $BinPath) {
    try {
        $ver = & $BinPath --version 2>$null
        if (-not $ver) { $ver = "$BinName 可执行" }
        Write-Ok $ver
    } catch {
        Write-Ok "$BinName 已就位"
    }
} else {
    Write-Err "$BinPath 不存在"
    exit 1
}

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
Write-Host ""
Write-Host "deepx 安装完成!" -ForegroundColor Green
Write-Host ""
Write-Host "  下一步:"
Write-Host "    1. 新开 PowerShell / Windows Terminal(让 PATH 生效)"
Write-Host "    2. 运行: deepx"
Write-Host "       首次启动会引导你配置 API key(写入 %USERPROFILE%\.deepx\model.yaml)"
Write-Host ""
Write-Host "  卸载: Remove-Item -Recurse -Force '$BinDir', `"$env:USERPROFILE\.deepx`""
Write-Host ""
