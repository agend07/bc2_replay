@echo off
setlocal
cd /d "%~dp0"
echo BC2 recorder live monitor (read-only). Ctrl+C to stop.
echo During play the demo files stay 0 bytes; data is buffered in RAM.
echo A disk flush happens at round end / map change.
python "%~dp0BC2RecorderMonitor.py" %*
pause
endlocal