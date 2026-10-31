@echo off
rem Test exe name avoids "patch"/"setup": Windows installer detection would demand elevation.
rem Build the shared (client + replay server) dinput8.dll into rome_proxy\out_shared\.
rem Needs VS C++ x86 tools.
setlocal
cd /d "%~dp0"
for /f "usebackq delims=" %%i in (`call "%ProgramFiles(x86)%\Microsoft Visual Studio\Installer\vswhere.exe" -latest -products * -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath`) do set VS=%%i
if not defined VS for /d %%d in ("%ProgramFiles%\Microsoft Visual Studio\2022\*") do if exist "%%d\VC\Auxiliary\Build\vcvars32.bat" set "VS=%%d"
call "%VS%\VC\Auxiliary\Build\vcvars32.bat" >nul || exit /b 1
if not exist out_shared mkdir out_shared
set SRC=client_probe.c client_recorder.c bc2_ring.c bc2_capture.c client_chat.c client_smoke.c client_smoke_hook.c client_fx.c client_dump.c
cl /nologo /Brepro /O2 /W4 /LD /GS- dinput8_proxy.c %SRC% /Fo:out_shared\ /Fe:out_shared\dinput8.dll /link /Brepro /DEF:dinput8.def kernel32.lib user32.lib bcrypt.lib || exit /b 1
cl /nologo /Brepro /O2 /W4 /GS- /DBC2_CHAT_SELFTEST client_chat_selftest.c client_chat.c client_smoke.c client_smoke_hook.c client_fx.c /Fo:out_shared\ /Fe:out_shared\client_chat_selftest.exe /link /Brepro kernel32.lib user32.lib || exit /b 1
cl /nologo /Brepro /O2 /W4 /GS- client_probe_selftest.c /Fo:out_shared\ /Fe:out_shared\client_probe_selftest.exe /link /Brepro kernel32.lib user32.lib bcrypt.lib || exit /b 1
cl /nologo /Brepro /O2 /W4 /GS- bc2_ring_selftest.c bc2_ring.c bc2_capture.c /Fo:out_shared\ /Fe:out_shared\bc2_ring_selftest.exe /link /Brepro kernel32.lib user32.lib bcrypt.lib || exit /b 1
cl /nologo /Brepro /O2 /W4 /GS- /DBC2_RECORDER_SELFTEST client_recorder_selftest.c client_recorder.c bc2_ring.c bc2_capture.c client_probe.c /Fo:out_shared\ /Fe:out_shared\client_recorder_selftest.exe /link /Brepro kernel32.lib user32.lib bcrypt.lib || exit /b 1
cl /nologo /Brepro /O2 /W4 /GS- client_dump_selftest.c client_dump.c /Fo:out_shared\ /Fe:out_shared\client_dump_selftest.exe /link /Brepro kernel32.lib user32.lib || exit /b 1
echo Built out_shared\dinput8.dll, client_probe_selftest.exe, bc2_ring_selftest.exe, client_recorder_selftest.exe, client_chat_selftest.exe and client_dump_selftest.exe
