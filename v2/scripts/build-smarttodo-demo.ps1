param(
    [string]$OutputDir = "dist/smarttodo-demo"
)

$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force $OutputDir | Out-Null
Copy-Item -Recurse -Force examples/smarttodo/web/* $OutputDir
Copy-Item -Force (Join-Path (go env GOROOT) 'lib\wasm\wasm_exec.js') (Join-Path $OutputDir 'wasm_exec.js')

Push-Location examples/smarttodo
$env:GOOS = 'js'
$env:GOARCH = 'wasm'
$wasmOut = Join-Path (Join-Path '..\..' $OutputDir) 'smarttodo.wasm'
go build -o $wasmOut ./cmd/smarttodo-wasm
Pop-Location

Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
Write-Host "Built Smart Todo demo into $OutputDir"
