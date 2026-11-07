<#
.SYNOPSIS
    PulseFeed 测试线第一批验收入口（默认不依赖 Docker）。

.DESCRIPTION
    依次运行三条回归测试，并输出每条耗时与最终退出码：
      1) ActionWorker 重复投递幂等
      2) Feed 页缓存 singleflight 并发合并 + 跨用户 Viewer 隔离
      3) 热榜分钟桶同分钟累加 / 跨分钟翻转
    任一测试失败，脚本以退出码 1 结束；全部通过退出码为 0。

.PARAMETER Integration
    可选手工段，默认关闭。开启后脚本不会拉起任何进程或容器，只对已经启动的环境
    发 100 个并发 Feed 请求，校验响应信封 code=0 且各请求条目一致。
    启用前请自行启动 api 与 worker，例如：
        scripts/start.sh
    或
        docker compose -f apps/docker-compose.yml up -d

.PARAMETER ApiBase
    -Integration 使用的 API 基地址，默认 http://localhost:8090。

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/acceptance-tests.ps1

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/acceptance-tests.ps1 -Integration -ApiBase http://localhost:8090

.NOTES
    前置条件：
      - Go 1.22+ 已加入 PATH；
      - 首次运行需要访问模块代理以下载 miniredis（仅测试依赖，进程内 Redis，无需 Docker）；
      - 默认路径不需要 Docker、MySQL、Redis、RabbitMQ，也不会修改仓库文件。
#>
param(
    [switch]$Integration,
    [string]$ApiBase = 'http://localhost:8090'
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$apiDir = Join-Path $repoRoot 'apps/api'
if (-not (Test-Path (Join-Path $apiDir 'go.mod'))) {
    Write-Host "找不到 Go 模块目录: $apiDir" -ForegroundColor Red
    exit 2
}

$suites = @(
    @{
        Name    = 'ActionWorker 重复投递幂等'
        Package = './internal/application/interaction'
        Pattern = 'TestActionWorker'
    },
    @{
        Name    = 'Feed 页缓存并发合并与跨用户隔离'
        Package = './internal/application/feed'
        Pattern = 'TestFeedPageCacheSingleflightMergesConcurrentMisses|TestFeedSharedCacheEntriesIsolateViewersAcrossUsers'
    },
    @{
        Name    = '热榜分钟桶累加与跨分钟翻转'
        Package = './internal/infra/cache'
        Pattern = 'TestHotBuckets'
    }
)

$failed = New-Object System.Collections.Generic.List[string]
$overall = [System.Diagnostics.Stopwatch]::StartNew()

foreach ($suite in $suites) {
    Write-Host ''
    Write-Host ("==> " + $suite.Name) -ForegroundColor Cyan
    $watch = [System.Diagnostics.Stopwatch]::StartNew()
    Push-Location $apiDir
    try {
        go test $suite.Package -run $suite.Pattern -count=1
        $code = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    $watch.Stop()
    $seconds = [Math]::Round($watch.Elapsed.TotalSeconds, 2)
    if ($code -eq 0) {
        Write-Host ("    PASS  " + $seconds + "s") -ForegroundColor Green
    } else {
        Write-Host ("    FAIL (exit " + $code + ")  " + $seconds + "s") -ForegroundColor Red
        $failed.Add($suite.Name)
    }
}

if ($Integration) {
    Write-Host ''
    Write-Host ("==> [Integration] 并发请求 " + $ApiBase + "/api/feed（需预先启动 api/worker）") -ForegroundColor Cyan
    $watch = [System.Diagnostics.Stopwatch]::StartNew()
    try {
        Add-Type -AssemblyName System.Net.Http
        $client = New-Object System.Net.Http.HttpClient
        $client.Timeout = [TimeSpan]::FromSeconds(10)
        $uri = $ApiBase.TrimEnd('/') + '/api/feed?scene=latest&limit=5'

        $tasks = @()
        for ($i = 0; $i -lt 100; $i++) {
            $tasks += $client.GetStringAsync($uri)
        }
        [System.Threading.Tasks.Task]::WaitAll([System.Threading.Tasks.Task[]]$tasks)

        $firstIDs = $null
        $bad = 0
        foreach ($task in $tasks) {
            $payload = $task.Result | ConvertFrom-Json
            if ($payload.code -ne 0) {
                $bad++
                continue
            }
            $ids = @($payload.data.items | ForEach-Object { $_.clip_id })
            $joined = ($ids -join ',')
            if ($null -eq $firstIDs) {
                $firstIDs = $joined
            } elseif ($joined -ne $firstIDs) {
                $bad++
            }
        }
        $client.Dispose()

        $watch.Stop()
        $seconds = [Math]::Round($watch.Elapsed.TotalSeconds, 2)
        if ($bad -gt 0) {
            Write-Host ("    FAIL  不一致或非 code=0 响应 " + $bad + " 个  " + $seconds + "s") -ForegroundColor Red
            $failed.Add('[Integration] 并发 Feed 响应不一致')
        } else {
            Write-Host ("    PASS  100 个并发请求全部一致  " + $seconds + "s") -ForegroundColor Green
        }
    } catch {
        $watch.Stop()
        Write-Host ("    FAIL  API 不可达或请求异常: " + $_.Exception.Message) -ForegroundColor Red
        $failed.Add('[Integration] API 探活/并发请求')
    }
}

$overall.Stop()
$totalSeconds = [Math]::Round($overall.Elapsed.TotalSeconds, 2)

Write-Host ''
if ($failed.Count -gt 0) {
    Write-Host ("验收失败（" + $failed.Count + " 项）：" + ($failed -join '；')) -ForegroundColor Red
    Write-Host ("总耗时 " + $totalSeconds + "s")
    exit 1
}
Write-Host ("全部通过，总耗时 " + $totalSeconds + "s") -ForegroundColor Green
exit 0
