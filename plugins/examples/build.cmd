:: build.cmd -- build the SkyLoader sample plugins into Windows x64 DLLs.
::
:: Open a "x64 Native Tools Command Prompt for VS" and run this script for the
:: MSVC toolchain; otherwise it falls back to g++ (MinGW-w64) or zig when those
:: are on PATH. Output lands next to each plugin.json.
::
:: usage:
::   build.cmd                     build all samples
::   build.cmd 01-field-settings   build one sample
@echo off
setlocal enabledelayedexpansion

set "ROOT=%~dp0"
set "INC1=%ROOT%..\..\libraries\SkyBootstrap\include"
set "INC2=%ROOT%common"
set "ALL=01-field-settings 02-list-collection 03-struct-state"

set "CHOICE=%~1"
if "%CHOICE%"=="" set "CHOICE=%ALL%"

set "TOOLCHAIN="
where cl >nul 2>nul && set "TOOLCHAIN=cl"
if not defined TOOLCHAIN ( where g++ >nul 2>nul && set "TOOLCHAIN=g++" )
if not defined TOOLCHAIN ( where zig >nul 2>nul && set "TOOLCHAIN=zig" )
if not defined TOOLCHAIN (
  echo error: no compiler found.
  echo   Use an MSVC x64 prompt, or install MinGW-w64 or Zig and add to PATH.
  exit /b 1
)
echo toolchain: %TOOLCHAIN%

for %%S in (%CHOICE%) do call :build "%%S"
goto :eof

:build
set "SAMPLE=%~1"
if not exist "%ROOT%%SAMPLE%" (
  echo error: unknown sample "%SAMPLE%"
  exit /b 1
)
for %%C in ("%ROOT%%SAMPLE%\*.cpp") do (
  set "SRC=%%C"
  set "NAME=%%~nC"
)
set "OUT=%ROOT%%SAMPLE%\%NAME%.dll"
echo building %SAMPLE% -^> %NAME%.dll

if "%TOOLCHAIN%"=="cl" (
  cl /nologo /LD /EHsc /std:c++17 /O2 /I"%INC1%" /I"%INC2%" "!SRC!" /Fe:"%OUT%"
  move /y "%SAMPLE%\%NAME%.obj" "%TEMP%\skyloader-sample.obj" >nul 2>nul
) else if "%TOOLCHAIN%"=="g++" (
  rem A static runtime keeps the sample DLL free of libstdc++ dependencies.
  g++ -std=c++17 -O2 -shared -static -static-libgcc -static-libstdc++ ^
    -I"%INC1%" -I"%INC2%" "!SRC!" -o "%OUT%"
) else (
  zig c++ -std=c++17 -O2 -shared -target x86_64-windows-gnu ^
    -I"%INC1%" -I"%INC2%" "!SRC!" -o "%OUT%"
)
goto :eof
