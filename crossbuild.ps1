# crossbuild.ps1 - 交叉编译 wstunnel 到 6 个平台目标。
#
# 用法:
#   .\crossbuild.ps1              # 编译全部目标
#   .\crossbuild.ps1 -Clean       # 清理 binaries\
#
# PowerShell 版，Windows 上方便用。等价于 crossbuild.sh。

[CmdletBinding()]
param(
    [switch]$Clean
)

$ErrorActionPreference = 'Stop'

# 六个固定目标；armhf = 32 位 ARM，GOARM=6（兼容 ARMv6/v7，如树莓派）
$Targets = @(
    @{ GOOS = 'linux';   GOARCH = 'amd64'; Name = '';   GoARM = '' }
    @{ GOOS = 'linux';   GOARCH = 'arm64'; Name = '';   GoARM = '' }
    @{ GOOS = 'linux';   GOARCH = 'arm';   Name = 'armhf'; GoARM = '6' }
    @{ GOOS = 'windows'; GOARCH = 'amd64'; Name = '';   GoARM = '' }
    @{ GOOS = 'darwin';  GOARCH = 'arm64'; Name = '';   GoARM = '' }
    @{ GOOS = 'darwin';  GOARCH = 'amd64'; Name = '';   GoARM = '' }
)

# 版本号：优先 git tag，其次 short hash，最后 dev
$Version = 'dev'
try {
    $gitDesc = git describe --tags --always --dirty 2>$null
    if ($gitDesc) { $Version = $gitDesc.Trim() }
} catch { }

$LdFlags = "-s -w -X main.version=$Version"
$OutDir = 'binaries'

if ($Clean) {
    if (Test-Path $OutDir) { Remove-Item $OutDir -Recurse -Force }
    Write-Host "cleaned $OutDir"
    exit 0
}

if (-not (Test-Path $OutDir)) {
    New-Item -ItemType Directory -Path $OutDir | Out-Null
}

Write-Host "== building wstunnel $Version =="
foreach ($t in $Targets) {
    # 输出名: Name 缺省用 GOARCH（armhf 场景覆盖）
    $archName = if ($t.Name) { $t.Name } else { $t.GOARCH }
    $ext = if ($t.GOOS -eq 'windows') { '.exe' } else { '' }
    $out = "$OutDir/wstunnel-$($t.GOOS)-$archName$ext"
    Write-Host "--> $($t.GOOS)/$($t.GOARCH)$(if ($t.GoARM) { " (GOARM=$($t.GoARM))" }) -> $out"

    $env:GOOS = $t.GOOS
    $env:GOARCH = $t.GOARCH
    $env:CGO_ENABLED = '0'
    if ($t.GoARM) { $env:GOARM = $t.GoARM } else { $env:GOARM = $null }

    go build -trimpath -ldflags $LdFlags -o $out .

    if (Test-Path $out) {
        $size = (Get-Item $out).Length
        Write-Host ("    size: {0:N1} KB" -f ($size / 1KB))
    }
}

# 清理环境变量，避免影响后续 go build
$env:GOOS = $null
$env:GOARCH = $null
$env:GOARM = $null
$env:CGO_ENABLED = $null

Write-Host "== done. artifacts in $OutDir\ =="
Get-ChildItem $OutDir | ForEach-Object { Write-Host "  $($_.Name)" }
