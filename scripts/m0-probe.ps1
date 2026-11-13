<#
M0 feasibility probe for mhyAccelerator.

Measures, against the official CDN only:
  1. system DNS, with 1 / 4 / 16 / 32 parallel connections
  2. every CDN node IP found through DNS-over-HTTPS from several regions, 1 connection each
  3. the best node, and the best three nodes mixed, with 16 connections

Nothing is saved to disk: response bodies go to NUL. Needs curl.exe (ships with Windows 10+).
Messages are ASCII on purpose: Windows PowerShell 5.1 misreads UTF-8 scripts without a BOM.

Usage:
  powershell -ExecutionPolicy Bypass -File scripts\m0-probe.ps1
  powershell -ExecutionPolicy Bypass -File scripts\m0-probe.ps1 -Biz nap_cn -Duration 10
#>
param(
    [string]$Biz = 'hkrpg_cn',          # hk4e_cn | hkrpg_cn | nap_cn | bh3_cn
    [string]$Url = '',                  # test this URL instead of asking the launcher API
    [int]$Duration = 8,                 # seconds per multi-connection test
    [int]$ProbeSeconds = 5,             # seconds per single-node probe
    [int]$MaxIps = 12,                  # probe at most this many node IPs
    [int[]]$Conns = @(1, 4, 16, 32),
    [string]$OutFile = 'm0-result.json'
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$Api = 'https://hyp-api.mihoyo.com/hyp/hyp-connect/api/getGamePackages?launcher_id=jGHBHlcOq1'
$Fmt = '%{size_download}|%{time_starttransfer}|%{time_total}|%{http_code}'
$Inv = [cultureinfo]::InvariantCulture

# Client subnets sent to the resolvers so the CDN scheduler answers as if we were there.
$Views = [ordered]@{
    'cn-telecom' = '202.96.209.0/24'
    'cn-unicom'  = '202.106.0.0/24'
    'cn-mobile'  = '211.136.112.0/24'
    'hk'         = '203.80.96.0/24'
    'jp'         = '210.130.0.0/24'
    'sg'         = '165.21.83.0/24'
    'us'         = '129.250.35.0/24'
    'de'         = '194.25.0.0/24'
}
$Resolvers = @('https://dns.alidns.com/resolve', 'https://dns.google/resolve')

function Get-TestTarget {
    if ($Url) {
        $head = & curl.exe -sI -m 15 $Url
        $len = ($head | Where-Object { $_ -match '^content-length:\s*(\d+)' } | ForEach-Object { [long]$Matches[1] } | Select-Object -Last 1)
        if (-not $len) { throw "Cannot read Content-Length of $Url" }
        return [pscustomobject]@{ Url = $Url; Size = $len }
    }
    $resp = Invoke-RestMethod -Uri $Api -TimeoutSec 20
    if ($resp.retcode -ne 0) { throw "Launcher API error: $($resp.message)" }
    $game = $resp.data.game_packages | Where-Object { $_.game.biz -eq $Biz }
    if (-not $game) { throw "Game '$Biz' not found. Available: $(($resp.data.game_packages | ForEach-Object { $_.game.biz }) -join ', ')" }
    $pkg = $game.main.major.game_pkgs | Sort-Object { [long]$_.size } -Descending | Select-Object -First 1
    if (-not $pkg) { throw "The API lists no full package for '$Biz' (this game may be Sophon-only). Pass -Url." }
    Write-Host "Game $Biz version $($game.main.major.version)"
    return [pscustomobject]@{ Url = $pkg.url; Size = [long]$pkg.size }
}

function Resolve-Nodes([string]$HostName) {
    $found = @{}
    $add = {
        param($ip, $view)
        if ($ip -notmatch '^\d+\.\d+\.\d+\.\d+$') { return }
        if (-not $found.ContainsKey($ip)) { $found[$ip] = New-Object System.Collections.Generic.List[string] }
        if (-not $found[$ip].Contains($view)) { $found[$ip].Add($view) }
    }
    try {
        [System.Net.Dns]::GetHostAddresses($HostName) |
            Where-Object { $_.AddressFamily -eq 'InterNetwork' } |
            ForEach-Object { & $add $_.IPAddressToString 'system' }
    } catch { Write-Warning "System DNS failed: $($_.Exception.Message)" }

    foreach ($view in $Views.Keys) {
        foreach ($r in $Resolvers) {
            try {
                $q = "$r`?name=$HostName&type=A&edns_client_subnet=$($Views[$view])"
                $ans = Invoke-RestMethod -Uri $q -TimeoutSec 8 -Headers @{ accept = 'application/dns-json' }
                $ans.Answer | Where-Object { $_.type -eq 1 } | ForEach-Object { & $add $_.data $view }
            } catch { }
        }
    }
    return $found
}

# Runs $N parallel curl downloads for $Seconds and returns the aggregate throughput.
function Invoke-Burst {
    param([string]$TargetUrl, [long]$Size, [string[]]$Ips, [int]$N, [int]$Seconds)
    $u = [uri]$TargetUrl
    $tmp = Join-Path $env:TEMP ('m0probe_' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory $tmp | Out-Null
    try {
        $slice = [long][math]::Floor($Size / ($N + 1))
        $procs = @()
        for ($i = 0; $i -lt $N; $i++) {
            $a = "-s -o NUL --http1.1 -m $Seconds --connect-timeout 5 -r $($slice * $i)- -w `"$Fmt`""
            if ($Ips -and $Ips.Count -gt 0) { $a += " --resolve $($u.Host):443:$($Ips[$i % $Ips.Count])" }
            $a += " `"$TargetUrl`""
            $procs += Start-Process curl.exe -ArgumentList $a -NoNewWindow -PassThru `
                -RedirectStandardOutput (Join-Path $tmp "$i.out") -RedirectStandardError (Join-Path $tmp "$i.err")
        }
        $procs | Wait-Process

        $bytes = 0L; $rate = 0.0; $ok = 0; $ttfb = @()
        for ($i = 0; $i -lt $N; $i++) {
            $line = Get-Content (Join-Path $tmp "$i.out") -Raw -ErrorAction SilentlyContinue
            if (-not $line) { continue }
            $p = $line.Trim().Split('|')
            if ($p.Count -lt 4 -or $p[3] -ne '206') { continue }
            $b = [long]$p[0]
            $start = [double]::Parse($p[1], $Inv)
            $window = [double]::Parse($p[2], $Inv) - $start
            $ok++; $bytes += $b; $ttfb += $start
            if ($window -gt 0.2) { $rate += $b / $window }
        }
        $avgTtfb = $null
        if ($ttfb.Count -gt 0) { $avgTtfb = [math]::Round((($ttfb | Measure-Object -Average).Average) * 1000) }
        return [pscustomobject]@{
            Conns  = $N
            Ok     = $ok
            MBps   = [math]::Round($rate / 1MB, 2)      # megabytes per second
            Mbit   = [math]::Round($rate * 8 / 1e6, 1)  # megabits per second
            TtfbMs = $avgTtfb
            MB     = [math]::Round($bytes / 1MB, 1)
        }
    } finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
}

if (-not (Get-Command curl.exe -ErrorAction SilentlyContinue)) { throw 'curl.exe not found.' }

$target = Get-TestTarget
$hostName = ([uri]$target.Url).Host
Write-Host "Target $($target.Url)"
Write-Host ("Size   {0:N1} GB" -f ($target.Size / 1GB))
Write-Host ''

Write-Host "[1/3] System DNS, $($Conns -join ' / ') connections, $Duration s each"
$baseline = @()
foreach ($n in $Conns) {
    $r = Invoke-Burst -TargetUrl $target.Url -Size $target.Size -Ips @() -N $n -Seconds $Duration
    $baseline += $r
    Write-Host ("  {0,2} conn: {1,7} MB/s  ({2} ok)" -f $n, $r.MBps, $r.Ok)
}

Write-Host ''
Write-Host "[2/3] Resolving $hostName from $($Views.Count) regions"
$nodes = Resolve-Nodes $hostName
$ips = @($nodes.Keys | Sort-Object { $nodes[$_].Count } -Descending | Select-Object -First $MaxIps)
Write-Host "  $($nodes.Count) node IPs found, probing $($ips.Count) with 1 connection, $ProbeSeconds s each"
$probes = @()
foreach ($ip in $ips) {
    $r = Invoke-Burst -TargetUrl $target.Url -Size $target.Size -Ips @($ip) -N 1 -Seconds $ProbeSeconds
    $probes += [pscustomobject]@{
        IP = $ip; MBps = $r.MBps; TtfbMs = $r.TtfbMs; Ok = [bool]$r.Ok; Views = ($nodes[$ip] -join ',')
    }
    Write-Host ("  {0,-15} {1,7} MB/s  ttfb {2,5} ms  [{3}]" -f $ip, $r.MBps, $r.TtfbMs, ($nodes[$ip] -join ','))
}
$ranked = @($probes | Where-Object { $_.Ok } | Sort-Object MBps -Descending)

Write-Host ''
Write-Host "[3/3] Best nodes with 16 connections, $Duration s each"
$pinned = @()
if ($ranked.Count -gt 0) {
    $r = Invoke-Burst -TargetUrl $target.Url -Size $target.Size -Ips @($ranked[0].IP) -N 16 -Seconds $Duration
    $pinned += [pscustomobject]@{ Mode = "best node ($($ranked[0].IP))"; MBps = $r.MBps; Ok = $r.Ok }
    Write-Host ("  best node  {0,-15}: {1,7} MB/s  ({2} ok)" -f $ranked[0].IP, $r.MBps, $r.Ok)
    if ($ranked.Count -ge 2) {
        $top = @($ranked | Select-Object -First 3 | ForEach-Object { $_.IP })
        $r = Invoke-Burst -TargetUrl $target.Url -Size $target.Size -Ips $top -N 16 -Seconds $Duration
        $pinned += [pscustomobject]@{ Mode = "top $($top.Count) nodes mixed"; MBps = $r.MBps; Ok = $r.Ok }
        Write-Host ("  top {0} nodes mixed          : {1,7} MB/s  ({2} ok)" -f $top.Count, $r.MBps, $r.Ok)
    }
} else {
    Write-Host '  no node answered the probe'
}

Write-Host ''
Write-Host '==== Summary ===='
$single = ($baseline | Where-Object { $_.Conns -eq 1 } | Select-Object -First 1).MBps
$bestBase = $baseline | Sort-Object MBps -Descending | Select-Object -First 1
$bestPinned = $pinned | Sort-Object MBps -Descending | Select-Object -First 1
Write-Host ("1 connection, system DNS      : {0} MB/s" -f $single)
Write-Host ("best multi-conn, system DNS   : {0} MB/s at {1} connections" -f $bestBase.MBps, $bestBase.Conns)
if ($bestPinned) { Write-Host ("best pinned nodes, 16 conn    : {0} MB/s ({1})" -f $bestPinned.MBps, $bestPinned.Mode) }
if ($single -gt 0) {
    $all = @($bestBase.MBps); if ($bestPinned) { $all += $bestPinned.MBps }
    Write-Host ("speed-up over 1 connection    : x{0:N1}" -f ((($all | Measure-Object -Maximum).Maximum) / $single))
}

if ($OutFile) {
    [pscustomobject]@{
        time = (Get-Date).ToString('s'); biz = $Biz; url = $target.Url; size = $target.Size
        baseline = $baseline; probes = $probes; pinned = $pinned
    } | ConvertTo-Json -Depth 5 | Out-File -Encoding utf8 $OutFile
    Write-Host "Saved to $OutFile"
}
