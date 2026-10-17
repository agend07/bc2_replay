@echo off
setlocal
cd /d "%~dp0"
echo Wymuszam STOP nagrywania i zapis bufora na dysk...
python "%~dp0diagnostics\bc2_stop_recording.py" --confirm
echo.
echo Sprawdz pliki w folderze demos.
pause
endlocal