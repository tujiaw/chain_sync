@echo off
setlocal
set CGO_ENABLED=0
if not exist dist mkdir dist

echo ==^> windows/amd64
set GOOS=windows
set GOARCH=amd64
go build -trimpath -ldflags "-s -w" -o dist\chainsync-windows-amd64.exe .

echo ==^> linux/amd64
set GOOS=linux
set GOARCH=amd64
go build -trimpath -ldflags "-s -w" -o dist\chainsync-linux-amd64 .

echo ==^> linux/arm64
set GOARCH=arm64
go build -trimpath -ldflags "-s -w" -o dist\chainsync-linux-arm64 .

echo ==^> linux/armv7
set GOARCH=arm
set GOARM=7
go build -trimpath -ldflags "-s -w" -o dist\chainsync-linux-armv7 .

echo 完成
dir dist
