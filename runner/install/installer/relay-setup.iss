; Relay-Setup.exe - GUI wrapper around runner/install/install.ps1 (Inno Setup 6.3+).
;
; Build (CI does this, see .github/workflows/runner-release.yml job windows-installer):
;   ISCC /DAppVersion=runner-abc1234 /DSrcDir=dist relay-setup.iss
; SrcDir must contain keeperd-windows-amd64.exe, relay-runner-windows-amd64.exe,
; install-keeperd.ps1 and install.ps1. Output: Output\Relay-Setup.exe.
;
; Design: Inno does NOT install the binaries itself. It drops the files in {tmp}
; and runs install.ps1 (RELAY_INSTALL_FROM = that dir, RELAY_INSTALL_NOPAIR=1),
; which owns the real install into %LOCALAPPDATA%\Relay\bin plus the scheduled
; tasks. So there is no [Files] entry for {app} and the uninstaller is custom.

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
#ifndef SrcDir
  #define SrcDir "dist"
#endif

[Setup]
AppName=Relay
AppVersion={#AppVersion}
AppPublisher=Vinuitik
AppId={{6B0F2C1E-4D7A-4E58-9C33-52A1B7E0A1F4}
; Admin: install.ps1 registers an at-boot scheduled task. NOTE: {localappdata}
; is then the *elevating* account's profile (same assumption as install.ps1).
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
WizardStyle=modern
; Only the uninstaller lives here; real binaries are in ..\bin (install.ps1).
DefaultDirName={localappdata}\Relay\setup
DisableDirPage=yes
DisableProgramGroupPage=yes
DefaultGroupName=Relay
OutputBaseFilename=Relay-Setup
OutputDir=Output
Compression=lzma2
SolidCompression=yes
UninstallDisplayName=Relay
; ProgressPage-style wizard: no "Ready to install" fluff needed.
DisableReadyPage=yes

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Files]
; Staged in {tmp} (auto-deleted when setup exits); install.ps1 copies from there.
Source: "{#SrcDir}\keeperd-windows-amd64.exe";      DestDir: "{tmp}"; Flags: deleteafterinstall ignoreversion
Source: "{#SrcDir}\relay-runner-windows-amd64.exe"; DestDir: "{tmp}"; Flags: deleteafterinstall ignoreversion
Source: "{#SrcDir}\install-keeperd.ps1";            DestDir: "{tmp}"; Flags: deleteafterinstall ignoreversion
Source: "{#SrcDir}\install.ps1";                    DestDir: "{tmp}"; Flags: deleteafterinstall ignoreversion

[Icons]
; Console stays visible here on purpose: the user sees pairing progress.
Name: "{group}\Connect Relay to HabitTracker"; Filename: "{localappdata}\Relay\bin\relay-runner.exe"; Parameters: "pair --open"

[Run]
; Finish-page checkboxes (checked by default). Exactly one is shown.
Filename: "{localappdata}\Relay\bin\relay-runner.exe"; Parameters: "pair --open"; \
  Description: "Connect this computer to HabitTracker (opens your browser)"; \
  Flags: postinstall nowait runhidden skipifsilent; Check: not AlreadyPaired
Filename: "https://habittrackerdima.me/relay"; \
  Description: "Open HabitTracker (this computer is already connected)"; \
  Flags: postinstall nowait shellexec skipifsilent; Check: AlreadyPaired

[Code]
const
  PairFile = '\.relay\hub.json';

function AlreadyPaired: Boolean;
begin
  Result := FileExists(GetEnv('USERPROFILE') + PairFile);
end;

function LogPath: String;
begin
  Result := ExpandConstant('{localappdata}\Relay\install.log');
end;

// Writes run-install.ps1 into the temp dir: sets the contract env vars, runs install.ps1
// with all output into the log, exits 1 on any throw. A wrapper file avoids quoting hell and
// the stale-LASTEXITCODE trap of "-Command ... exit $LASTEXITCODE" (install.ps1 may run npm,
// which can leave a non-zero code behind even on success). install.ps1 fails by throwing.
function WriteWrapper(const Path: String): Boolean;
var
  S: String;
begin
  S :=
    '$ErrorActionPreference = ''Stop''' + #13#10 +
    '$env:RELAY_INSTALL_FROM = ''' + ExpandConstant('{tmp}') + '''' + #13#10 +
    '$env:RELAY_INSTALL_NOPAIR = ''1''' + #13#10 +
    'try {' + #13#10 +
    '  & ''' + ExpandConstant('{tmp}\install.ps1') + ''' *> ''' + LogPath + '''' + #13#10 +
    '} catch {' + #13#10 +
    '  ($_ | Out-String) | Add-Content -Path ''' + LogPath + '''' + #13#10 +
    '  exit 1' + #13#10 +
    '}' + #13#10 +
    'exit 0' + #13#10;
  Result := SaveStringToFile(Path, S, False);
end;

procedure RunInstallScript;
var
  Wrapper: String;
  Code: Integer;
begin
  ForceDirectories(ExpandConstant('{localappdata}\Relay'));
  DeleteFile(LogPath);
  Wrapper := ExpandConstant('{tmp}\run-install.ps1');
  if not WriteWrapper(Wrapper) then
    RaiseException('Could not prepare the installer script.');
  WizardForm.StatusLabel.Caption := 'Installing Relay and starting it...';
  WizardForm.ProgressGauge.Style := npbstMarquee;   { unknown duration }
  try
    if not Exec(ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe'),
        '-NoProfile -ExecutionPolicy Bypass -File "' + Wrapper + '"',
        '', SW_HIDE, ewWaitUntilTerminated, Code) then
      Code := -1;
  finally
    WizardForm.ProgressGauge.Style := npbstNormal;
  end;
  if Code <> 0 then
    RaiseException('Relay could not be installed (code ' + IntToStr(Code) + ').' + #13#10 + #13#10 +
      'Nothing is wrong with your computer. The details were saved to:' + #13#10 +
      LogPath + #13#10 + #13#10 +
      'Send that file to the person who gave you Relay.');
end;

{ A raised exception during ssPostInstall makes Setup show the message, roll back
  and exit with an error: that is the "friendly message and abort". }
procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
    RunInstallScript;
end;

{ ---------------- Uninstall ----------------
  Nothing install.ps1 created is tracked by Inno, so undo it by hand:
  stop+unregister both scheduled tasks, kill the processes, delete ..\bin,
  drop it from the user PATH. %USERPROFILE%\.relay (runner key + pairing) and
  relay.env are deliberately kept. }
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Script, Wrapper: String;
  Code: Integer;
begin
  if CurUninstallStep = usUninstall then
  begin
    Script :=
      '$ErrorActionPreference = ''SilentlyContinue''' + #13#10 +
      'foreach ($t in ''Relay keeper'', ''Relay input watcher'') {' + #13#10 +
      '  Stop-ScheduledTask -TaskName $t' + #13#10 +
      '  Unregister-ScheduledTask -TaskName $t -Confirm:$false' + #13#10 +
      '}' + #13#10 +
      'Get-Process keeperd, relay-runner | Stop-Process -Force' + #13#10 +
      'Start-Sleep -Seconds 2' + #13#10 +
      '$bin = Join-Path $env:LOCALAPPDATA ''Relay\bin''' + #13#10 +
      'Remove-Item -Path $bin -Recurse -Force' + #13#10 +
      '$p = [Environment]::GetEnvironmentVariable(''Path'', ''User'')' + #13#10 +
      'if ($p) {' + #13#10 +
      '  $keep = $p.Split('';'') | Where-Object { $_ -and ($_.TrimEnd(''\'') -ne $bin) }' + #13#10 +
      '  [Environment]::SetEnvironmentVariable(''Path'', ($keep -join '';''), ''User'')' + #13#10 +
      '}' + #13#10 +
      'exit 0' + #13#10;
    Wrapper := ExpandConstant('{tmp}\run-uninstall.ps1');
    if SaveStringToFile(Wrapper, Script, False) then
      Exec(ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe'),
        '-NoProfile -ExecutionPolicy Bypass -File "' + Wrapper + '"',
        '', SW_HIDE, ewWaitUntilTerminated, Code);
  end;
  if (CurUninstallStep = usPostUninstall) and (not UninstallSilent) then
    MsgBox('Relay was removed.' + #13#10 + #13#10 +
      'Your pairing and runner key were kept in:' + #13#10 +
      GetEnv('USERPROFILE') + '\.relay' + #13#10 +
      '(delete that folder yourself if you want a completely clean removal).',
      mbInformation, MB_OK);
end;
