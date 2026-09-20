# Launches the Relay runner hidden, bound to this laptop's Tailscale IP.
# Registered as a Scheduled Task (trigger: at logon) - it does NOT come back
# on its own after a shutdown/restart with nobody there: the task only fires
# when a human logs in, so after a headless reboot the runner stays down
# until someone logs into this Windows session. See runner/FLOWS.md "Windows
# auto-start (laptop)". Not a Windows service, just a background process
# relaunched at logon.
$env:RELAY_LISTEN_ADDR = "100.124.46.7:7777"

# Without this, RELAY_FCM_CREDENTIALS is unset, NewNotifier("") returns a
# no-op notifier, and job-done pushes to the phone silently never send (the
# runner just logs "FCM not configured"). $PSScriptRoot keeps this relative
# to the script's own location instead of a hardcoded user path.
$env:RELAY_FCM_CREDENTIALS = Join-Path $PSScriptRoot "fcm-service-account.json"

# Without this, POST /v1/suspend returns 503 and the phone's Sleep button
# can never work. Only set this on a machine you intend to let the phone
# actually put to sleep - see runner/FLOWS.md "Idle-suspend (S5)".
$env:RELAY_IDLE_SUSPEND_ENABLED = "true"

# Deliberately long on THIS machine. Idle-suspend measures Relay session
# activity only - internal/session.Manager.IdleStatus() has no idea whether a
# human is sat at the keyboard. On the server that is fine; on this laptop,
# which is also used directly, the 3-minute default would suspend the machine
# mid-work simply because no phone session was open. Until suspend also
# considers real user input (Win32 GetLastInputInfo), keep this long and use
# the phone's Sleep button for "I'm done" instead.
$env:RELAY_IDLE_TIMEOUT = "12h"

Start-Process -FilePath "C:\Users\sizon\OneDrive\Documents\Relay\runner\install\relay-runner-windows-amd64.exe" -WindowStyle Hidden
