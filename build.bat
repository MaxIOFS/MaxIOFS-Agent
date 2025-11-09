@echo off
echo Building MaxIOFS Agent with icon...

REM Ir al directorio del script
cd /d "%~dp0"

REM Verificar si existe rsrc.exe
where rsrc.exe >nul 2>&1
if %ERRORLEVEL% NEQ 0 (
    echo Installing rsrc tool...
    go install github.com/akavel/rsrc@latest
    echo.
    echo Please add Go bin to your PATH if not already done:
    echo set PATH=%%PATH%%;%%USERPROFILE%%\go\bin
    echo.
    pause
)

REM Ir al directorio del comando
cd cmd\maxiofs-agent

REM Generar archivo de recursos con icono
echo Embedding icon into executable...
rsrc -manifest manifest.xml -ico icon.ico -o rsrc.syso

if not exist rsrc.syso (
    echo ERROR: Failed to generate rsrc.syso
    echo Make sure icon.ico and manifest.xml exist
    pause
    exit /b 1
)

echo rsrc.syso generated successfully (%CD%\rsrc.syso)
dir rsrc.syso

REM Compilar DESDE el directorio del paquete main (para que Go encuentre rsrc.syso)
echo.
echo Compiling application from: %CD%
go build -ldflags="-H windowsgui" -o ..\..\maxiofs-agent.exe .

REM Volver al directorio raíz
cd ..\..

echo.
if exist maxiofs-agent.exe (
    echo ========================================
    echo Build completed successfully!
    echo ========================================
    echo Executable: %CD%\maxiofs-agent.exe
    echo Icon should be embedded in the executable.
    echo.
    echo Check the icon by:
    echo 1. Right-click maxiofs-agent.exe
    echo 2. Select Properties
    echo 3. Icon should appear in the dialog
    echo ========================================
) else (
    echo ERROR: Build failed - executable not created
)
echo.

