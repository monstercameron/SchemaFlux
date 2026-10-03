# run.ps1 - Build and run SmartTodo application

Write-Host "Building SmartTodo..." -ForegroundColor Cyan

# Build the application
go build -o smarttodo.exe ./cmd/smarttodo

if ($LASTEXITCODE -ne 0) {
    Write-Host "Build failed!" -ForegroundColor Red
    exit 1
}

Write-Host "Build successful!" -ForegroundColor Green
Write-Host ""
Write-Host "Starting SmartTodo..." -ForegroundColor Cyan
Write-Host ""

# Run the application
.\smarttodo.exe

# Capture exit code
$exitCode = $LASTEXITCODE

if ($exitCode -eq 0) {
    Write-Host ""
    Write-Host "SmartTodo closed successfully" -ForegroundColor Green
} else {
    Write-Host ""
    Write-Host "SmartTodo exited with code: $exitCode" -ForegroundColor Yellow
}

exit $exitCode
