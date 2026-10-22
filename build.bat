@echo off
rem SkyLoader build helper for Windows.
rem Mirrors the Makefile: launcher is built with Go, the DLL with cgo + Zig.
rem Usage: build.bat [launcher^|dll^|all^|clean]
setlocal EnableExtensions EnableDelayedExpansion
cd /d "%~dp0"

if not defined GO set "GO=go"
if not defined ZIG set "ZIG=zig"

if not "%~1"=="" goto :arg

:menu
cls
echo ==========================================
echo   SkyLoader build
echo ==========================================
echo.
echo   1. Build launcher (Go)   -^> dist\SkyLoader.exe
echo   2. Build DLL (Zig)       -^> dist\SkyBootstrap.dll
echo   3. Build all
echo.
echo   4. Clean
echo.
echo   0. Exit
echo.
set "choice="
ver >nul
set /p "choice=Select an option: "
if errorlevel 1 goto :end

if "!choice!"=="0" goto :end
if "!choice!"=="1" goto :menu_launcher
if "!choice!"=="2" goto :menu_dll
if "!choice!"=="3" goto :menu_all
if "!choice!"=="4" goto :menu_clean
if "!choice!"=="" goto :menu

echo.
echo Unknown option: !choice!
pause
goto :menu

:menu_launcher
call :launcher
goto :menu_done

:menu_dll
call :dll
goto :menu_done

:menu_all
call :launcher
if not errorlevel 1 call :dll
goto :menu_done

:menu_clean
call :clean
goto :menu_done

:menu_done
echo.
pause
goto :menu

:arg
if /i "%~1"=="launcher" (
    call :launcher
    goto :finish
)
if /i "%~1"=="dll" (
    call :dll
    goto :finish
)
if /i "%~1"=="all" (
    call :launcher
    if not errorlevel 1 call :dll
    goto :finish
)
if /i "%~1"=="clean" (
    call :clean
    goto :finish
)
echo Unknown target: %~1
echo Usage: build.bat [launcher^|dll^|all^|clean]
exit /b 2

rem ---------------------------------------------------------------- launcher
:launcher
setlocal
echo.
echo [launcher] Checking Go toolchain...
"%GO%" version >nul 2>&1
if errorlevel 1 (
    echo [launcher] Go not found. Install Go 1.22+ and add it to PATH.
    goto :target_failed
)

set "CGO_ENABLED=0"
set "GOOS=windows"
set "GOARCH=amd64"
echo [launcher] Generating Windows resources...
"%GO%" run ./resources/genwinres.go
if errorlevel 1 goto :target_failed

if not exist dist mkdir dist
if not exist dist goto :target_failed

echo [launcher] Building dist\SkyLoader.exe...
"%GO%" build -tags "desktop,production" -trimpath -buildvcs=false -ldflags="-s -w -H=windowsgui" -o dist\SkyLoader.exe ./cmd/skyloader
if errorlevel 1 goto :target_failed

echo [launcher] OK: dist\SkyLoader.exe
endlocal
exit /b 0

rem --------------------------------------------------------------------- dll
:dll
setlocal
call :check_go
if errorlevel 1 goto :target_failed
call :setup_zig
if errorlevel 1 goto :target_failed

if not exist dist mkdir dist
if not exist dist goto :target_failed

echo [dll] Building dist\SkyBootstrap.dll with zig cc...
"%GO%" build -buildmode=c-shared -trimpath -buildvcs=false -ldflags="-s -w" -o dist\SkyBootstrap.dll ./cmd/skybootstrap
if errorlevel 1 goto :target_failed

if exist dist\SkyBootstrap.h del /q dist\SkyBootstrap.h
echo [dll] OK: dist\SkyBootstrap.dll
endlocal
exit /b 0

:check_go
"%GO%" version >nul 2>&1
if not errorlevel 1 exit /b 0
echo Go not found. Open a new terminal after installation, or set GO to go.exe.
exit /b 1

:setup_zig
echo.
echo [toolchain] Checking Zig...
"%ZIG%" version >nul 2>&1
if errorlevel 1 (
    echo [dll] Zig not found. Install Zig 0.13+ and add it to PATH,
    echo [dll] or point ZIG at it, e.g. set ZIG=C:\tools\zig\zig.exe
    exit /b 1
)

set "CGO_ENABLED=1"
set "GOOS=windows"
set "GOARCH=amd64"
set "CC="%ZIG%" cc -target x86_64-windows-gnu"
exit /b 0

rem ------------------------------------------------------------------- clean
:clean
echo.
echo [clean] Removing build output...
if exist dist rmdir /s /q dist
if exist dist goto :failed
if exist cmd\skyloader\rsrc_windows_amd64.syso del /q cmd\skyloader\rsrc_windows_amd64.syso
if exist cmd\skyloader\rsrc_windows_amd64.syso goto :failed
echo [clean] OK
exit /b 0

:target_failed
endlocal
:failed
echo.
echo Build failed.
exit /b 1

:finish
if errorlevel 1 exit /b 1
goto :end

:end
endlocal
exit /b 0
