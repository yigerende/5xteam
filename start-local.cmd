@echo off
setlocal EnableExtensions DisableDelayedExpansion
chcp 65001 >nul
cd /d "%~dp0"

set "PS_EXE=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"
if not exist "%PS_EXE%" set "PS_EXE=pwsh.exe"

if not exist "run-logs" mkdir "run-logs"

for /f "tokens=5" %%P in ('netstat -ano ^| findstr /R /C:":18120 .*LISTENING"') do set "EXISTING_PID=%%P"
if defined EXISTING_PID (
  echo [OK] Service is already listening on port 18120. PID %EXISTING_PID%
  start "" "http://127.0.0.1:18120/"
  exit /b 0
)

where go >nul 2>nul
if errorlevel 1 (
  echo [ERROR] Go was not found in PATH.
  exit /b 1
)

echo Building chatgpt-space-merge...
go build -o chatgpt-space-merge.exe ./cmd/server
if errorlevel 1 (
  echo [ERROR] Build failed.
  exit /b 1
)

set "APP_ADDR=127.0.0.1:18120"
set "APP_DATA_DIR=%CD%\data"
set "STDOUT=%CD%\run-logs\server.out.log"
set "STDERR=%CD%\run-logs\server.err.log"

"%PS_EXE%" -NoProfile -ExecutionPolicy Bypass -Command "$process = Start-Process -FilePath '%CD%\chatgpt-space-merge.exe' -WorkingDirectory '%CD%' -RedirectStandardOutput '%STDOUT%' -RedirectStandardError '%STDERR%' -WindowStyle Hidden -PassThru; Set-Content -Path '%CD%\run-logs\server.pid' -Value $process.Id"

"%PS_EXE%" -NoProfile -ExecutionPolicy Bypass -Command "$ready=$false; for($i=0;$i -lt 40;$i++){ Start-Sleep -Milliseconds 250; try { $r=Invoke-WebRequest -UseBasicParsing 'http://127.0.0.1:18120/health/ready' -TimeoutSec 1; if($r.StatusCode -eq 200){$ready=$true;break} } catch {} }; if(-not $ready){exit 1}"
if errorlevel 1 (
  echo [ERROR] Startup failed. Check run-logs\server.err.log.
  exit /b 1
)

echo [OK] Started: http://127.0.0.1:18120/
exit /b 0
