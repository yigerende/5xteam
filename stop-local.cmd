@echo off
setlocal EnableExtensions DisableDelayedExpansion
chcp 65001 >nul
cd /d "%~dp0"

set "PS_EXE=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"
if not exist "%PS_EXE%" set "PS_EXE=pwsh.exe"

if not exist "run-logs\server.pid" (
  echo [INFO] No local PID file was found.
  exit /b 0
)

set /p SERVER_PID=<"run-logs\server.pid"
"%PS_EXE%" -NoProfile -ExecutionPolicy Bypass -Command "$expected=(Resolve-Path '.\chatgpt-space-merge.exe' -ErrorAction SilentlyContinue).Path; $process=Get-Process -Id %SERVER_PID% -ErrorAction SilentlyContinue; if(-not $process){Write-Host '[INFO] Service was not running.'; exit 0}; if(-not $expected -or $process.Path -ne $expected){Write-Error 'PID file does not point to this project executable.'; exit 1}; Stop-Process -Id %SERVER_PID%; Write-Host '[OK] Service stopped.'"
if errorlevel 1 (
  echo [ERROR] Refused to stop an unrelated process.
  exit /b 1
)
del /q "run-logs\server.pid" >nul 2>nul
exit /b 0
