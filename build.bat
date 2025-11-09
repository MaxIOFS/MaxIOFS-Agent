@echo off
set CGO_ENABLED=1
set CGO_CFLAGS=-IC:/Users/aricardo/Projects/MaxIOFS-Agent/include
set CGO_LDFLAGS=-LC:/Program Files (x86)/WinFsp/lib -lwinfsp-x64
go build -ldflags="-H windowsgui" -o maxiofs-agent.exe ./cmd/maxiofs-agent
