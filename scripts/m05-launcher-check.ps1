<#
M0.5 check for mhyAccelerator: does the official launcher follow the hosts file,
and how many connections does it open while downloading?

Actions:
  watch    Sample the launcher's connections and the machine's download rate. Changes nothing.
  pin      Find the fastest CDN node for one game and pin it in the hosts file.   (administrator)
  revert   Remove everything this script added to the hosts file.                 (administrator)
  status   Show what is currently pinned.

Suggested run (administrator PowerShell, from the repository root):
  1. Start a download in the launcher, then:   .\scripts\m05-launcher-check.ps1 watch
  2. Pause the download, close the launcher:   .\scripts\m05-launcher-check.ps1 pin -Biz hkrpg_cn
  3. Reopen the launcher, resume, then:        .\scripts\m05-launcher-check.ps1 watch
  4. When finished:                            .\scripts\m05-launcher-check.ps1 revert

The script's own probe traffic is discarded (written to NUL). What the launcher downloads
is a real download and stays in the game folder.
Messages are ASCII on purpose: Windows PowerShell 5.1 misreads UTF-8 scripts without a BOM.
#>
param(
    [Parameter(Position = 0)][ValidateSet('watch', 'pin', 'revert', 'status', 'block', 'unblock', 'flushcache')][string]$Action = 'status',
    [string[]]$DnsIps = @('203.107.0.0/16'),   # HTTPDNS ranges to block for the launcher (block action)
    [string]$Biz = 'hkrpg_cn',                 # hk4e_cn | hkrpg_cn | nap_cn | bh3_cn
    [string]$Ip = '',                          # pin this IP instead of probing
    [string]$LauncherDir = 'C:\Program Files\miHoYo Launcher',   # change if the launcher is installed elsewhere
    [int]$Seconds = 30,                        # watch duration
    [int]$MaxIps = 12,
    [int]$ProbeSeconds = 4
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$HostsPath = Join-Path $env:SystemRoot 'System32\drivers\etc\hosts'
$BackupPath = "$HostsPath.mhyacc.bak"
$Begin = '# >>> mhyAccelerator (remove with: m05-launcher-check.ps1 revert)'
$End = '# <<< mhyAccelerator'
$Domains = @{
    'hk4e_cn'  = 'autopatchcn.yuanshen.com'
    'hkrpg_cn' = 'autopatchcn.bhsr.com'
    'nap_cn'   = 'autopatchcn.juequling.com'
    'bh3_cn'   = 'autopatchcn.bh3.com'
}
$Views = [ordered]@{
    'cn-telecom' = '202.96.209.0/24'; 'cn-unicom' = '202.106.0.0/24'; 'cn-mobile' = '211.136.112.0/24'
    'hk' = '203.80.96.0/24'; 'jp' = '210.130.0.0/24'; 'sg' = '165.21.83.0/24'
    'us' = '129.250.35.0/24'; 'de' = '194.25.0.0/24'
}
$Resolvers = @('https://dns.alidns.com/resolve', 'https://dns.google/resolve')

function Test-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    return (New-Object Security.Principal.WindowsPrincipal $id).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-PinnedEntries {
    $inside = $false; $out = @()
    foreach ($line in [IO.File]::ReadAllLines($HostsPath)) {
        if ($line -eq $Begin) { $inside = $true; continue }
        if ($line -eq $End) { $inside = $false; continue }
        if ($inside -and $line -match '^\s*(\S+)\s+(\S+)') { $out += [pscustomobject]@{ IP = $Matches[1]; Host = $Matches[2] } }
    }
    return $out
}

# Rewrites the hosts file without our block, then appends $Entries (if any) as a new block.
function Set-PinnedEntries([object[]]$Entries) {
    if (-not (Test-Admin)) { throw 'Run this from an administrator PowerShell.' }
    if (-not (Test-Path $BackupPath)) { Copy-Item $HostsPath $BackupPath }
    $keep = New-Object System.Collections.Generic.List[string]
    $inside = $false
    foreach ($line in [IO.File]::ReadAllLines($HostsPath)) {
        if ($line -eq $Begin) { $inside = $true; continue }
        if ($line -eq $End) { $inside = $false; continue }
        if (-not $inside) { $keep.Add($line) }
    }
    while ($keep.Count -gt 0 -and $keep[$keep.Count - 1].Trim() -eq '') { $keep.RemoveAt($keep.Count - 1) }
    if ($Entries -and $Entries.Count -gt 0) {
        $keep.Add(''); $keep.Add($Begin)
        foreach ($e in $Entries) { $keep.Add("$($e.IP) $($e.Host)") }
        $keep.Add($End)
    }
    [IO.File]::WriteAllLines($HostsPath, $keep, [Text.Encoding]::ASCII)
    & ipconfig.exe /flushdns | Out-Null
}

function Find-BestNode([string]$HostName) {
    $resp = Invoke-RestMethod -Uri 'https://hyp-api.mihoyo.com/hyp/hyp-connect/api/getGamePackages?launcher_id=jGHBHlcOq1' -TimeoutSec 20
    $game = $resp.data.game_packages | Where-Object { $_.game.biz -eq $Biz }
    $pkg = $game.main.major.game_pkgs | Sort-Object { [long]$_.size } -Descending | Select-Object -First 1
    if (-not $pkg) { throw "No test file for '$Biz'. Pass -Ip to pin a node by hand." }
    $url = $pkg.url
    if (([uri]$url).Host -ne $HostName) { throw "Test file is on $(([uri]$url).Host), expected $HostName." }

    $seen = @{}
    foreach ($view in $Views.Keys) {
        foreach ($r in $Resolvers) {
            try {
                $ans = Invoke-RestMethod -TimeoutSec 8 -Headers @{ accept = 'application/dns-json' } `
                    -Uri "$r`?name=$HostName&type=A&edns_client_subnet=$($Views[$view])"
                $ans.Answer | Where-Object { $_.type -eq 1 -and $_.data -match '^\d+\.\d+\.\d+\.\d+$' } |
                    ForEach-Object { $seen[$_.data] = 1 + [int]$seen[$_.data] }
            } catch { }
        }
    }
    $ips = @($seen.Keys | Sort-Object { $seen[$_] } -Descending | Select-Object -First $MaxIps)
    Write-Host "$($seen.Count) node IPs found for $HostName, probing $($ips.Count) ($ProbeSeconds s each)"
    $inv = [cultureinfo]::InvariantCulture
    $best = $null
    foreach ($ip in $ips) {
        $o = & curl.exe -s -o NUL --http1.1 -m $ProbeSeconds --connect-timeout 4 -r '1073741824-' `
            --resolve "${HostName}:443:$ip" -w '%{size_download}|%{time_starttransfer}|%{time_total}|%{http_code}' $url
        $p = "$o".Split('|'); $rate = 0.0
        if ($p.Count -eq 4 -and $p[3] -eq '206') {
            $window = [double]::Parse($p[2], $inv) - [double]::Parse($p[1], $inv)
            if ($window -gt 0.2) { $rate = [long]$p[0] / $window / 1MB }
        }
        Write-Host ("  {0,-15} {1,7:N2} MB/s" -f $ip, $rate)
        if ($rate -gt 0 -and (-not $best -or $rate -gt $best.Rate)) { $best = [pscustomobject]@{ IP = $ip; Rate = $rate } }
    }
    if (-not $best) { throw 'No node answered the probe.' }
    return $best.IP
}

function Get-RxBytes {
    $sum = 0L
    Get-NetAdapterStatistics -ErrorAction SilentlyContinue | ForEach-Object { $sum += [long]$_.ReceivedBytes }
    return $sum
}

function Invoke-Watch {
    $dir = $LauncherDir.TrimEnd('\') + '\'
    $pinned = @(Get-PinnedEntries)
    $pinnedIps = @($pinned | ForEach-Object { $_.IP })
    if ($pinned.Count -gt 0) { Write-Host "Pinned: $(($pinned | ForEach-Object { "$($_.Host) -> $($_.IP)" }) -join '; ')" }
    else { Write-Host 'Pinned: nothing (baseline)' }
    if (-not (Test-Admin)) { Write-Warning 'Not administrator: some launcher processes may be invisible.' }
    Write-Host "Sampling for $Seconds s. The launcher must be downloading now."
    Write-Host ''

    $perIp = @{}; $peak = 0; $samples = 0; $names = @{}
    $rx0 = Get-RxBytes; $t0 = Get-Date
    while (((Get-Date) - $t0).TotalSeconds -lt $Seconds) {
        $procIds = @{}
        Get-Process -ErrorAction SilentlyContinue | ForEach-Object {
            $path = $null
            try { $path = $_.Path } catch { }
            if ($path -and $path.StartsWith($dir, [StringComparison]::OrdinalIgnoreCase)) { $procIds[[int]$_.Id] = $_.Name }
        }
        $conns = @(Get-NetTCPConnection -State Established -ErrorAction SilentlyContinue |
            Where-Object { $procIds.ContainsKey([int]$_.OwningProcess) -and ($_.RemotePort -eq 443 -or $_.RemotePort -eq 80) -and
                           $_.RemoteAddress -notmatch '^(127\.|::1$)' })
        $now = @{}
        foreach ($c in $conns) {
            $key = "$($c.RemoteAddress):$($c.RemotePort)"
            $now[$key] = 1 + [int]$now[$key]
            $names[$procIds[[int]$c.OwningProcess]] = $true
        }
        foreach ($k in $now.Keys) { if ([int]$perIp[$k] -lt $now[$k]) { $perIp[$k] = $now[$k] } }
        if ($conns.Count -gt $peak) { $peak = $conns.Count }
        $samples++
        Start-Sleep -Milliseconds 1500
    }
    $elapsed = ((Get-Date) - $t0).TotalSeconds
    $rate = ((Get-RxBytes) - $rx0) / $elapsed / 1MB

    if ($perIp.Count -eq 0) {
        Write-Host "No launcher connections seen under $dir. Is a download running? Is -LauncherDir right?"
        return
    }
    Write-Host 'Remote endpoint            peak conns   note'
    foreach ($k in ($perIp.Keys | Sort-Object { $perIp[$_] } -Descending)) {
        $ipOnly = $k.Substring(0, $k.LastIndexOf(':'))
        $note = ''
        if ($pinnedIps -contains $ipOnly) { $note = 'PINNED node' }
        elseif ($ipOnly -match ':') { $note = 'IPv6' }
        Write-Host ("{0,-26} {1,10}   {2}" -f $k, $perIp[$k], $note)
    }
    Write-Host ''
    Write-Host "Launcher processes with connections : $(($names.Keys | Sort-Object) -join ', ')"
    Write-Host "Peak simultaneous connections       : $peak"
    Write-Host ("Machine-wide download rate          : {0:N2} MB/s (all adapters, all programs)" -f $rate)
    if ($pinned.Count -gt 0) {
        $hitConns = 0; $allConns = 0
        foreach ($k in $perIp.Keys) {
            $allConns += $perIp[$k]
            if ($pinnedIps -contains $k.Substring(0, $k.LastIndexOf(':'))) { $hitConns += $perIp[$k] }
        }
        $share = [math]::Round(100.0 * $hitConns / [math]::Max(1, $allConns))
        Write-Host "Connections to the pinned node      : $hitConns of $allConns ($share%)"
        if ($share -ge 70) { Write-Host 'Result: the launcher FOLLOWS the hosts file.' }
        elseif ($hitConns -gt 0) { Write-Host 'Result: PARTIAL. Most connections bypass hosts (own HTTPDNS, IPv6, or other domains).' }
        else { Write-Host 'Result: no connection to the pinned node. The launcher ignores hosts, or was not restarted after pinning.' }
    }
}

switch ($Action) {
    'status' {
        $p = @(Get-PinnedEntries)
        if ($p.Count -eq 0) { Write-Host 'Nothing pinned.' } else { $p | Format-Table -AutoSize }
    }
    'revert' {
        Set-PinnedEntries @()
        Write-Host "Hosts file restored. Original backup kept at $BackupPath"
    }
    'pin' {
        if (-not (Test-Admin)) { throw 'Run this from an administrator PowerShell.' }
        if (-not $Domains.ContainsKey($Biz)) { throw "Unknown game '$Biz'." }
        $hostName = $Domains[$Biz]
        if (-not $Ip) { $Ip = Find-BestNode $hostName }
        elseif ($Ip -notmatch '^\d+\.\d+\.\d+\.\d+$') { throw "'$Ip' is not an IPv4 address." }
        $others = @(Get-PinnedEntries | Where-Object { $_.Host -ne $hostName })
        Set-PinnedEntries (@($others) + @([pscustomobject]@{ IP = $Ip; Host = $hostName }))
        Write-Host "Pinned $hostName -> $Ip"
        Write-Host 'Restart the launcher so it resolves the name again, resume the download, then run: watch'
    }
    'watch' { Invoke-Watch }
    'block' {
        # Firewall-blocks the launcher's HTTPDNS so it may fall back to system DNS (and the hosts file).
        if (-not (Test-Admin)) { throw 'Run this from an administrator PowerShell.' }
        $exes = @(Get-ChildItem $LauncherDir -Recurse -Include 'HYP.exe', 'HYPHelper.exe', 'HYPWorker.exe' -ErrorAction SilentlyContinue)
        if ($exes.Count -eq 0) { throw "No launcher executables found under $LauncherDir" }
        Get-NetFirewallRule -Group 'mhyAccelerator' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
        foreach ($e in $exes) {
            New-NetFirewallRule -DisplayName "mhyAccelerator block HTTPDNS ($($e.Name))" -Group 'mhyAccelerator' `
                -Direction Outbound -Action Block -Program $e.FullName -RemoteAddress $DnsIps | Out-Null
            Write-Host "Blocked $($e.FullName) -> $($DnsIps -join ', ')"
        }
        Write-Host 'Restart the launcher, resume the download, then run: watch'
    }
    'flushcache' {
        # The launcher's download module (Chromium network stack) persists its DNS cache across restarts,
        # which can hide a hosts-file change. Move the cache file aside; the launcher recreates it.
        $running = @(Get-Process -ErrorAction SilentlyContinue | Where-Object {
            $p = $null; try { $p = $_.Path } catch { }
            $p -and $p.StartsWith($LauncherDir.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase) })
        if ($running.Count -gt 0) { throw "Exit the launcher first (still running: $(($running | ForEach-Object { $_.Name } | Sort-Object -Unique) -join ', '))." }
        $prefs = Join-Path $env:APPDATA 'miHoYo\HYP\1_1\modules\sophon\sophon_net\prefs\local_prefs.json'
        if (Test-Path $prefs) {
            Move-Item $prefs "$prefs.mhyacc.bak" -Force
            Write-Host "Launcher DNS cache moved to $prefs.mhyacc.bak"
        } else { Write-Host 'No launcher DNS cache file found.' }
        & ipconfig.exe /flushdns | Out-Null
    }
    'unblock' {
        if (-not (Test-Admin)) { throw 'Run this from an administrator PowerShell.' }
        Get-NetFirewallRule -Group 'mhyAccelerator' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
        Write-Host 'Firewall rules removed.'
    }
}
