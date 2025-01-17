@echo off
cd ..
set CGO_ENABLED = 1
set GOOS = windows
set GOEXPERIMENT=arenas

if not exist go.mod (
	echo Missing dependencies, please run get.cmd
	echo.
	pause
	exit
)
if not exist bin (
	MKDIR bin
) 

echo Building Ikemen GO...
echo. 

go build  -ldflags "-s -w -H=windowsgui"  -trimpath -v  -o ./bin/Ikemen-GGPO.exe ./src

pause