# Installs (or upgrades) the Relay runner on Windows. One command, in an
# ELEVATED PowerShell (Run as administrator), as the user who will run it:
#
#   irm https://github.com/Vinuitik/relay/releases/latest/download/install.ps1 | iex
#
# Steps:
#   1. Downloads keeperd.exe, relay-runner.exe and install-keeperd.ps1 from the
#      latest GitHub release, checks them against that release's SHA256SUMS
#   2. Tailscale is OPTIONAL and never installed here: if installed and logged
#      in, the runner listens on the Tailscale IP (direct phone-app access,
#      Android QR, firewall rule for port 7777); otherwise on 127.0.0.1:7777
#      and the machine is reached through the HabitTracker hub (step 7)
#   3. claude / codex CLIs via npm if npm exists and they are missing
#   4. Stops a running keeperd/runner, puts the binaries in %LOCALAPPDATA%\Relay\bin
#   5. Writes %LOCALAPPDATA%\Relay\relay.env if missing
#   6. Runs install-keeperd.ps1 (the single source of the "Relay keeper" /
#      "Relay input watcher" scheduled tasks), which starts keeperd
#   7. Unless already paired (hub.json exists): relay-runner pair - prints a QR,
#      scan it with your phone and tap Confirm in HabitTracker. Ctrl-C skips.
# Re-running it is an upgrade.
#
# Runs as one script block: piped through iex there is no $PSScriptRoot and
# no param(), and `exit` would close the user's window - so it only `return`s
# or throws.
& {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'   # PS 5.1 downloads crawl with the progress bar on
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $repo = if ($env:RELAY_UPDATE_REPO) { $env:RELAY_UPDATE_REPO } else { 'Vinuitik/relay' }
    $installCmd = "irm https://github.com/$repo/releases/latest/download/install.ps1 | iex"

    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        Write-Host "This needs an elevated PowerShell (registering the at-boot task needs admin)." -ForegroundColor Yellow
        Write-Host "Right-click PowerShell -> Run as administrator, then run again:"
        Write-Host "  $installCmd"
        return
    }

    $dir = Join-Path $env:LOCALAPPDATA 'Relay'
    $bin = Join-Path $dir 'bin'
    $keeper = Join-Path $bin 'keeperd.exe'
    $runner = Join-Path $bin 'relay-runner.exe'
    $envFile = Join-Path $dir 'relay.env'
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('relay-install-' + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $tmp | Out-Null

    # relay.env value (last KEY=... line), or $null.
    function Get-EnvValue([string]$key) {
        if (-not (Test-Path $envFile)) { return $null }
        $v = $null
        foreach ($l in Get-Content $envFile) {
            if ($l -match "^\s*$key\s*=(.*)$") { $v = $Matches[1].Trim().Trim('"') }
        }
        return $v
    }

    try {
        Write-Host "1/7 downloading (user $env:USERDOMAIN\$env:USERNAME)"
        if ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64') {
            Write-Host "    note: only windows-amd64 builds exist; this is $env:PROCESSOR_ARCHITECTURE (ARM64 runs them emulated)"
        }
        # Pin the tag first so binaries and SHA256SUMS come from the same release.
        $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" -Headers @{ 'User-Agent' = 'relay-install' }
        $tag = $rel.tag_name
        if (-not $tag) { throw "no release found at https://github.com/$repo/releases" }
        Write-Host "    release $tag"
        $base = "https://github.com/$repo/releases/download/$tag"
        $names = @('keeperd-windows-amd64.exe', 'relay-runner-windows-amd64.exe', 'install-keeperd.ps1')
        foreach ($n in @('SHA256SUMS') + $names) {
            Invoke-WebRequest -Uri "$base/$n" -OutFile (Join-Path $tmp $n) -UseBasicParsing
        }
        $sums = Get-Content (Join-Path $tmp 'SHA256SUMS')
        foreach ($n in $names) {
            $want = $null
            foreach ($l in $sums) {
                if ($l -match ('^([0-9a-f]{64})\s+\*?' + [regex]::Escape($n) + '$')) { $want = $Matches[1] }
            }
            if (-not $want) { throw "$n is not listed in SHA256SUMS of $tag" }
            $got = (Get-FileHash -Path (Join-Path $tmp $n) -Algorithm SHA256).Hash.ToLower()
            if ($got -ne $want) { throw "${n}: SHA256 mismatch - not installing" }
            Write-Host "    $n verified"
        }

        Write-Host "2/7 checking Tailscale (optional)"
        $tsIp = $null
        $ts = (Get-Command tailscale -ErrorAction SilentlyContinue | Select-Object -First 1).Source
        if (-not $ts) {
            $p = Join-Path $env:ProgramFiles 'Tailscale\tailscale.exe'
            if (Test-Path $p) { $ts = $p }
        }
        if ($ts) {
            try { $out = & $ts ip -4 2>$null } catch { $out = $null }
            if ($LASTEXITCODE -eq 0 -and $out) { $tsIp = ([string]($out | Select-Object -First 1)).Trim() }
            if ($tsIp) { Write-Host "    logged in, tailnet IP $tsIp" }
            else { Write-Host "    installed but not logged in - skipping (hub only)" }
        } else {
            Write-Host "    not installed - skipping (the HabitTracker hub doesn't need it)"
        }

        Write-Host "3/7 checking claude / codex CLIs"
        if (Get-Command npm -ErrorAction SilentlyContinue) {
            foreach ($cli in @(@('claude', '@anthropic-ai/claude-code'), @('codex', '@openai/codex'))) {
                if (Get-Command $cli[0] -ErrorAction SilentlyContinue) {
                    Write-Host "    $($cli[0]) CLI already installed"
                } else {
                    Write-Host "    installing $($cli[0]) CLI (npm install -g $($cli[1]))"
                    & npm install -g $cli[1]
                    if ($LASTEXITCODE -ne 0) { Write-Host "    $($cli[0]) CLI install failed - continuing" }
                }
            }
        } else {
            Write-Host "    npm not found - skipping. Install Node.js (https://nodejs.org), then:"
            Write-Host "    npm install -g @anthropic-ai/claude-code @openai/codex"
        }

        Write-Host "4/7 installing binaries to $bin"
        foreach ($t in 'Relay keeper', 'Relay input watcher') {
            if (Get-ScheduledTask -TaskName $t -ErrorAction SilentlyContinue) { Stop-ScheduledTask -TaskName $t }
        }
        Get-Process keeperd, relay-runner -ErrorAction SilentlyContinue | Stop-Process -Force
        Start-Sleep -Seconds 2
        New-Item -ItemType Directory -Path $bin -Force | Out-Null
        foreach ($pair in @(@('keeperd-windows-amd64.exe', $keeper), @('relay-runner-windows-amd64.exe', $runner))) {
            $src = Join-Path $tmp $pair[0]; $dest = $pair[1]
            try {
                Copy-Item -Path $src -Destination $dest -Force
            } catch {
                # Still locked by a process we couldn't stop: a running exe can be
                # renamed, not overwritten. keeperd's clearOld deletes .old-* later.
                Move-Item -Path $dest -Destination ("$dest.old-" + [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()) -Force
                Copy-Item -Path $src -Destination $dest -Force
            }
        }

        Write-Host "5/7 settings ($envFile)"
        if (-not (Test-Path $envFile)) {
            Set-Content -Path $envFile -Encoding ASCII -Value @(
                '# Relay runner settings, read by keeperd at start.',
                '# After editing: restart the "Relay keeper" scheduled task (or reboot).',
                '# Every setting, documented:',
                "#   https://github.com/$repo/blob/main/runner/install/runner.env.example",
                '',
                '# Direct phone-app access over Tailscale: this machine''s Tailscale IP.',
                '# Unset = 127.0.0.1:7777, reachable only through the HabitTracker hub.',
                '#RELAY_LISTEN_ADDR=100.x.y.z:7777'
            )
            Write-Host "    wrote $envFile"
        }
        if ($tsIp -and -not (Get-EnvValue 'RELAY_LISTEN_ADDR')) {
            Add-Content -Path $envFile -Encoding ASCII -Value "RELAY_LISTEN_ADDR=${tsIp}:7777"
            Write-Host "    RELAY_LISTEN_ADDR set to ${tsIp}:7777 (Tailscale)"
        } else {
            Write-Host "    $envFile left as is"
        }
        $listen = Get-EnvValue 'RELAY_LISTEN_ADDR'
        $relayHome = Get-EnvValue 'RELAY_HOME'
        if (-not $relayHome) { $relayHome = Join-Path $env:USERPROFILE '.relay' }
        $hub = Get-EnvValue 'RELAY_HUB'

        if ($listen -and $listen -notmatch '^(127\.|localhost)') {
            # Tailscale's adapter is a "Public" network; without this Windows
            # silently blocks the phone (runner/FLOWS.md "Windows Firewall").
            $rule = 'Relay runner (7777)'
            if (-not (Get-NetFirewallRule -DisplayName $rule -ErrorAction SilentlyContinue)) {
                New-NetFirewallRule -DisplayName $rule -Direction Inbound -Protocol TCP -LocalPort 7777 -Action Allow -Profile Any | Out-Null
                Write-Host "    firewall: allowed inbound TCP 7777"
            }
        }

        Write-Host "6/7 registering scheduled tasks (install-keeperd.ps1)"
        # As a script block, not `& file.ps1`: execution policy (Restricted by
        # default) blocks .ps1 files but not code already in memory.
        & ([scriptblock]::Create((Get-Content -Path (Join-Path $tmp 'install-keeperd.ps1') -Raw)))
    } finally {
        Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }

    $keyFile = Join-Path $relayHome 'key.txt'
    $key = $null
    for ($i = 0; $i -lt 15 -and -not $key; $i++) {
        if (Test-Path $keyFile) { $key = (Get-Content $keyFile -Raw).Trim() } else { Start-Sleep -Seconds 1 }
    }

    Write-Host ''
    Write-Host '================================================================'
    Write-Host ' RELAY INSTALL COMPLETE'
    Write-Host '================================================================'
    if ($key) { Write-Host " Runner key  : $key" }
    else { Write-Host " Runner key  : not created yet - see $dir\keeper.log" }
    if ($listen) { Write-Host " Runner addr : $listen" }
    else { Write-Host ' Runner addr : 127.0.0.1:7777 (local only - reach it through the hub)' }
    Write-Host " logs        : $dir\keeper.log"
    Write-Host '================================================================'
    Write-Host ''

    $oldHome = $env:RELAY_HOME; $oldListen = $env:RELAY_LISTEN_ADDR; $oldHub = $env:RELAY_HUB
    try {
        $env:RELAY_HOME = $relayHome
        if ($hub) { $env:RELAY_HUB = $hub }
        if ($key -and $listen -and $listen -notmatch '^(127\.|localhost)') {
            Write-Host 'Android app (direct over Tailscale): Add Runner -> Scan QR:'
            $env:RELAY_LISTEN_ADDR = $listen
            & $runner -qr
            $env:RELAY_LISTEN_ADDR = $oldListen
            Write-Host ''
        }

        Write-Host '7/7 pairing with HabitTracker'
        # shared/HUB.md "QR pairing". The running keeperd's runner picks up
        # hub.json by itself (no restart). Last step on purpose: Ctrl-C here
        # stops only this, everything else is already installed.
        $hubFile = Join-Path $relayHome 'hub.json'
        $later = "    pair later with: & `"`$env:LOCALAPPDATA\Relay\bin\relay-runner.exe`" pair"
        if (Test-Path $hubFile) {
            Write-Host "    already paired ($hubFile) - skipping"
        } else {
            Write-Host '    Ctrl-C skips.'
            Write-Host ''
            try { & $runner pair } finally {
                if (-not (Test-Path $hubFile)) { Write-Host '    pairing skipped'; Write-Host $later }
            }
        }
    } finally {
        $env:RELAY_HOME = $oldHome; $env:RELAY_LISTEN_ADDR = $oldListen; $env:RELAY_HUB = $oldHub
    }
}
