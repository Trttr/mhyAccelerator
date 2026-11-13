<#
Records, every few seconds, how the official launcher is downloading while mhyacc runs:
  - machine-wide download rate (a stand-in for the launcher's total: CDN via us + PCDN + anything else)
  - the rate and node reported by the mhyacc forwarder (parsed from its console output file)
  - how many launcher connections go through the forwarder versus elsewhere (PCDN, APIs)

Output: a CSV under logs\, one row per sample. Stops when the forwarder exits or after -Hours.
Messages are ASCII on purpose: Windows PowerShell 5.1 misreads UTF-8 scripts without a BOM.

Usage:
  powershell -ExecutionPolicy Bypass -File scripts\session-logger.ps1 -ForwarderLog <path> -ForwarderPid <pid>
#>
param(
    [Parameter(Mandatory)][string]$ForwarderLog,
    [int]$ForwarderPid = 0,
    [int]$Interval = 10,
    [double]$Hours = 10,
    [string]$LauncherDir = 'C:\Program Files\miHoYo Launcher',   # change if the launcher is installed elsewhere
    [string]$OutDir = ''
)

$ErrorActionPreference = 'Continue'
# $PSScriptRoot is empty inside param() defaults on Windows PowerShell 5.1, so resolve here.
if (-not $OutDir) { $OutDir = Join-Path (Split-Path -Parent $MyInvocation.MyCommand.Path) '..\logs' }
New-Item -ItemType Directory -Force $OutDir | Out-Null
$out = Join-Path $OutDir ("session-{0}.csv" -f (Get-Date -Format 'yyyyMMdd-HHmm'))
# machine_minus_us_MBps is total machine traffic minus ours: it includes protocol overhead and
# other programs, so it overstates PCDN. For the real PCDN share use the launcher's own counter
# (current_download_byte in sophon.log) against the forwarder's cumulative bytes.
'time,launcher_running,total_MBps,via_us_MBps,machine_minus_us_MBps,via_us_conns,other_conns,other_endpoints,node,node_score_MBps,forwarder_line' |
    Out-File -Encoding utf8 $out

function Get-RxBytes {
    $sum = 0L
    Get-NetAdapterStatistics -ErrorAction SilentlyContinue | ForEach-Object { $sum += [long]$_.ReceivedBytes }
    $sum
}

$dir = $LauncherDir.TrimEnd('\') + '\'
$deadline = (Get-Date).AddHours($Hours)
$rxPrev = Get-RxBytes
$tPrev = Get-Date
$fwdBytesPrev = $null

while ((Get-Date) -lt $deadline) {
    Start-Sleep -Seconds $Interval
    if ($ForwarderPid -gt 0 -and -not (Get-Process -Id $ForwarderPid -ErrorAction SilentlyContinue)) {
        "# forwarder process $ForwarderPid exited at $(Get-Date -Format s)" | Out-File -Append -Encoding utf8 $out
        break
    }
    $now = Get-Date
    $secs = ($now - $tPrev).TotalSeconds
    $rx = Get-RxBytes
    $total = ($rx - $rxPrev) / $secs / 1e6
    $rxPrev = $rx; $tPrev = $now

    # Forwarder: cumulative MB in its latest status line gives an exact rate over our interval.
    $line = ''
    try { $line = Get-Content $ForwarderLog -Tail 30 -Encoding UTF8 -ErrorAction Stop | Where-Object { $_ -match 'MB/s' -and $_ -match '\|' } | Select-Object -Last 1 } catch { }
    $viaUs = ''; $node = ''; $score = ''
    if ($line -match '\s(\d+)\s*MB\s+') {
        # the cumulative figure is the number right before " MB " (not "MB/s")
        $cum = [double]([regex]::Matches($line, '(\d+)\s+MB(?!/)') | Select-Object -Last 1).Groups[1].Value
        if ($null -ne $fwdBytesPrev) { $viaUs = [math]::Round([math]::Max(0, ($cum - $fwdBytesPrev)) / $secs, 2) }
        $fwdBytesPrev = $cum
    }
    if ($line -match '\|\s*([0-9.]+)(/h2)?\s+([0-9.]+)\(') { $node = $Matches[1]; $score = $Matches[3] }

    # Launcher connections.
    $pids = @{}
    Get-Process -ErrorAction SilentlyContinue | ForEach-Object {
        $p = $null; try { $p = $_.Path } catch { }
        if ($p -and $p.StartsWith($dir, [StringComparison]::OrdinalIgnoreCase)) { $pids[[int]$_.Id] = 1 }
    }
    $conns = @(Get-NetTCPConnection -State Established -ErrorAction SilentlyContinue |
        Where-Object { $pids.ContainsKey([int]$_.OwningProcess) -and ($_.RemotePort -eq 443 -or $_.RemotePort -eq 80) })
    $us = @($conns | Where-Object { $_.RemoteAddress -eq '127.66.0.1' }).Count
    $others = @($conns | Where-Object { $_.RemoteAddress -ne '127.66.0.1' -and $_.RemoteAddress -notmatch '^(127\.|::1$)' })
    $otherEps = ($others | ForEach-Object { "$($_.RemoteAddress):$($_.RemotePort)" } | Sort-Object -Unique) -join ' '

    $otherRate = ''
    if ($viaUs -ne '') { $otherRate = [math]::Round([math]::Max(0, $total - $viaUs), 2) }
    $fields = @(
        $now.ToString('s'), ($pids.Count -gt 0), [math]::Round($total, 2), $viaUs, $otherRate,
        $us, $others.Count, "`"$otherEps`"", $node, $score, "`"$($line -replace '"', "'")`""
    )
    ($fields -join ',') | Out-File -Append -Encoding utf8 $out
}
