# Launches the Relay runner hidden, bound to this laptop's Tailscale IP.
# Registered as a Scheduled Task (trigger: at logon) so the runner comes back
# on its own after a full shutdown/restart - see runner/FLOWS.md "Windows
# auto-start (laptop)". Not a Windows service, just a background process
# relaunched at logon.
$env:RELAY_LISTEN_ADDR = "100.124.46.7:7777"
Start-Process -FilePath "C:\Users\sizon\OneDrive\Documents\Relay\runner\install\relay-runner-windows-amd64.exe" -WindowStyle Hidden
