@echo off
setlocal
cd /d "%~dp0"
if not exist "Frost.Game.Main_Win32_Final.RecordingTest.exe" (
  echo Patched test executable is missing. Run python BuildBC2RecordingTest.py first.
  pause
  exit /b 1
)
if not exist "Instance-bc2\ServerOptions.ini" (
  echo Instance-bc2 configuration is missing.
  pause
  exit /b 1
)
if not exist "demos\" mkdir "demos"
if not exist "demos\" (
  echo Could not create the demos directory.
  pause
  exit /b 1
)
set "BC2_RECSTAMP="
for /f %%I in ('powershell.exe -NoProfile -Command "Get-Date -Format yyyyMMdd_HHmmss_fff"') do set "BC2_RECSTAMP=%%I"
if not defined BC2_RECSTAMP (
  echo Could not generate a recording name. Server was not started.
  pause
  exit /b 1
)
set "BC2_RECBASE=demos/patched_%BC2_RECSTAMP%"
if exist "%BC2_RECBASE%.*" (
  echo Recording files already exist for this name. Server was not started.
  pause
  exit /b 1
)
echo Experimental native recorder activation test.
echo Recording base: %BC2_RECBASE%
echo Close any other test server before continuing on port 19567.
echo Read BC2_RECORDING_PATCH_TEST.txt for the test procedure.
echo Output capture was observed; replay fidelity and playback remain unverified.
start "" /wait "%~dp0Frost.Game.Main_Win32_Final.RecordingTest.exe" -server -dedicated -serverInstancePath "Instance-bc2/" -mapPack2Enabled 1 -port 19567 -timeStampLogNames -Demo.RecordDemoFileName "%BC2_RECBASE%"
echo Server process has exited. Preserve all matching files and the runtime log.
if exist "%BC2_RECBASE%.*" (
  dir /b "%BC2_RECBASE%.*"
) else (
  echo No files found for the original recording base. Check demos and the runtime log.
)
echo A forced stop may leave an unfinished recording.
pause
endlocal
