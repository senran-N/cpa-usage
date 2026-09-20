@echo off
setlocal enabledelayedexpansion

echo ========================================================
echo    CPA Usage & Billing Plugin Setup for Windows
echo ========================================================

set SCRIPT_DIR=%~dp0
set CPA_USAGE_DIR=%SCRIPT_DIR%..
set CPA_DIR=%CPA_USAGE_DIR%\..\CLIProxyAPI
set PLUGINS_DIR=%CPA_DIR%\plugins
set CONFIG_FILE=%CPA_DIR%\config.yaml

echo [*] Target plugins directory: %PLUGINS_DIR%
if not exist "%PLUGINS_DIR%" (
    mkdir "%PLUGINS_DIR%"
)

if exist "%CPA_USAGE_DIR%\cpa_usage.dll" (
    echo [*] Copying cpa_usage.dll to plugins\cpa-usage.dll...
    copy /Y "%CPA_USAGE_DIR%\cpa_usage.dll" "%PLUGINS_DIR%\cpa-usage.dll" >nul
    echo [OK] Plugin DLL deployed.
) else (
    echo [!] cpa_usage.dll not found in %CPA_USAGE_DIR%!
    echo [*] Please run build first: go build -buildmode=c-shared -o cpa_usage.dll .
)

echo [*] Applying Observe Group patch to management.html...
python "%SCRIPT_DIR%patch_observe.py" --fetch

echo.
echo ========================================================
echo Recommended config.yaml configuration for CPA:
echo ========================================================
echo plugins:
echo   enabled: true
echo   dir: "plugins"
echo   configs:
echo     cpa-usage:
echo       enabled: true
echo       db_path: "data/cpa_usage.db"
echo       retention_days: 90
echo ========================================================
echo.
echo Setup completed! Start or restart CLIProxyAPI to use.
pause
