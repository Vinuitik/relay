# One-time setup of keeperd on a Windows runner machine. Run in an ELEVATED
# PowerShell (registering an at-boot task for your user needs admin):
#
#   powershell -ExecutionPolicy Bypass -File install-keeperd.ps1
#
# Expects keeperd.exe (and optionally relay-runner.exe) already in
# %LOCALAPPDATA%\Relay\bin - keeperd downloads the runner itself if missing -
# and settings in %LOCALAPPDATA%\Relay\relay.env. After this, nothing on
# this machine needs updating by hand: keeperd pulls new releases.
#
# Registers two tasks:
#   "Relay keeper"        at boot, no logon needed (S4U: runs as you without
#                         a stored password - fine, Claude's login is a plain
#                         file; but no access to the desktop session).
#   "Relay input watcher" at logon, in your desktop session - tells the runner
#                         when you're using the keyboard/mouse, which the
#                         boot-time keeper can't see (see keeperd -watch-input).
# And retires the old at-logon runner task (start-relay-runner.ps1).
param([string]$User = "$env:USERDOMAIN\$env:USERNAME")
$ErrorActionPreference = "Stop"

$dir = Join-Path $env:LOCALAPPDATA "Relay"
$keeper = Join-Path $dir "bin\keeperd.exe"
if (-not (Test-Path $keeper)) { throw "$keeper is missing - put keeperd.exe there first" }

# Retire the old setup: its task and its running runner (it holds the port).
Get-ScheduledTask | Where-Object {
    ($_.Actions | ForEach-Object { "$($_.Execute) $($_.Arguments)" }) -match 'start-relay-runner|relay-runner-windows'
} | ForEach-Object {
    Write-Host "Removing old task $($_.TaskName)"
    Unregister-ScheduledTask -TaskName $_.TaskName -TaskPath $_.TaskPath -Confirm:$false
}
# The old setup was actually a Startup-folder shortcut running start-relay-runner.ps1.
$startup = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\Startup"
$shell = New-Object -ComObject WScript.Shell
Get-ChildItem $startup -Filter *.lnk -ErrorAction SilentlyContinue | Where-Object {
    $l = $shell.CreateShortcut($_.FullName); "$($l.TargetPath) $($l.Arguments)" -match 'relay'
} | ForEach-Object {
    Write-Host "Removing old startup shortcut $($_.Name)"
    Remove-Item $_.FullName
}
Get-Process relay-runner-windows-amd64, relay-runner, keeperd -ErrorAction SilentlyContinue | Stop-Process -Force

# No 3-day time limit (the default would kill keeperd), keep running on
# battery, restart if it ever crashes.
$settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable `
    -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -MultipleInstances IgnoreNew

Register-ScheduledTask -TaskName "Relay keeper" -Force `
    -Action (New-ScheduledTaskAction -Execute $keeper) `
    -Trigger (New-ScheduledTaskTrigger -AtStartup) `
    -Principal (New-ScheduledTaskPrincipal -UserId $User -LogonType S4U -RunLevel Limited) `
    -Settings $settings | Out-Null

Register-ScheduledTask -TaskName "Relay input watcher" -Force `
    -Action (New-ScheduledTaskAction -Execute $keeper -Argument "-watch-input") `
    -Trigger (New-ScheduledTaskTrigger -AtLogOn -User $User) `
    -Principal (New-ScheduledTaskPrincipal -UserId $User -LogonType Interactive -RunLevel Limited) `
    -Settings $settings | Out-Null

Start-ScheduledTask -TaskName "Relay keeper"
Start-ScheduledTask -TaskName "Relay input watcher"
Start-Sleep 5
Get-ScheduledTask -TaskName "Relay keeper", "Relay input watcher" | Select-Object TaskName, State
Write-Host "Logs: $dir\keeper.log"
