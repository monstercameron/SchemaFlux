#!/bin/bash

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}     SchemaFlux Test Suite Runner${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# Function to run tests with different options
run_test() {
    local test_name=$1
    local test_cmd=$2
    
    echo -e "${YELLOW}Running: $test_name${NC}"
    echo -e "${YELLOW}Command: $test_cmd${NC}"
    echo "----------------------------------------"
    
    if eval $test_cmd; then
        echo -e "${GREEN}✓ $test_name passed${NC}"
    else
        echo -e "${RED}✗ $test_name failed${NC}"
    fi
    echo ""
}

# 1. Quick test run (no coverage)
if [[ "$1" == "quick" ]]; then
    echo -e "${BLUE}Quick Test Run (no coverage)${NC}"
    echo ""
    go test -short ./...
    exit $?
fi

# 2. Run tests with coverage
if [[ "$1" == "coverage" ]] || [[ -z "$1" ]]; then
    echo -e "${BLUE}Running tests with coverage...${NC}"
    echo ""
    
    # Run tests and generate coverage profile
    go test -coverprofile=coverage.out -covermode=atomic ./...
    TEST_RESULT=$?
    
    if [ $TEST_RESULT -eq 0 ]; then
        echo -e "${GREEN}All tests passed!${NC}"
    else
        echo -e "${RED}Some tests failed!${NC}"
    fi
    
    # Display coverage percentage
    echo ""
    echo -e "${BLUE}Coverage Summary:${NC}"
    go tool cover -func=coverage.out | grep total | awk '{print "Total Coverage: " $3}'
    
    # Show uncovered lines per file
    echo ""
    echo -e "${BLUE}Coverage by file:${NC}"
    go tool cover -func=coverage.out | grep -E "\.go" | sort -t':' -k2 -V | column -t
    
    # Generate HTML coverage report
    if [[ "$2" == "html" ]]; then
        echo ""
        echo -e "${BLUE}Generating HTML coverage report...${NC}"
        go tool cover -html=coverage.out -o coverage.html
        echo -e "${GREEN}Coverage report saved to coverage.html${NC}"
        
        # Open in browser (macOS)
        if [[ "$OSTYPE" == "darwin"* ]]; then
            open coverage.html
        fi
    fi
    
    exit $TEST_RESULT
fi

# 3. Run specific test
if [[ "$1" == "test" ]] && [[ ! -z "$2" ]]; then
    echo -e "${BLUE}Running specific test: $2${NC}"
    echo ""
    go test -v -run "$2" ./...
    exit $?
fi

# 4. Run tests with race detection
if [[ "$1" == "race" ]]; then
    echo -e "${BLUE}Running tests with race detection...${NC}"
    echo ""
    go test -race ./...
    exit $?
fi

# 5. Run benchmarks
if [[ "$1" == "bench" ]]; then
    echo -e "${BLUE}Running benchmarks...${NC}"
    echo ""
    go test -bench=. -benchmem ./...
    exit $?
fi

# 6. Verbose test output
if [[ "$1" == "verbose" ]]; then
    echo -e "${BLUE}Running tests with verbose output...${NC}"
    echo ""
    go test -v ./...
    exit $?
fi

# 7. Run tests and show only failures
if [[ "$1" == "failures" ]]; then
    echo -e "${BLUE}Running tests (showing only failures)...${NC}"
    echo ""
    go test ./... 2>&1 | grep -E "FAIL|Error:|panic:" || echo -e "${GREEN}All tests passed!${NC}"
    exit ${PIPESTATUS[0]}
fi

# 8. Clean test cache and run
if [[ "$1" == "clean" ]]; then
    echo -e "${BLUE}Cleaning test cache and running tests...${NC}"
    echo ""
    go clean -testcache
    go test -cover ./...
    exit $?
fi

# 9. Run tests continuously (watch mode)
if [[ "$1" == "watch" ]]; then
    echo -e "${BLUE}Running tests in watch mode...${NC}"
    echo -e "${YELLOW}Press Ctrl+C to exit${NC}"
    echo ""
    
    # Check if fswatch is installed
    if ! command -v fswatch &> /dev/null; then
        echo -e "${RED}fswatch is not installed. Install it with: brew install fswatch${NC}"
        exit 1
    fi
    
    # Watch for changes and run tests
    fswatch -o . -e ".*" -i "\\.go$" | while read; do
        clear
        echo -e "${BLUE}Changes detected, running tests...${NC}"
        go test -short ./...
        echo ""
        echo -e "${YELLOW}Waiting for changes...${NC}"
    done
fi

# 10. Generate coverage badge (requires gocov-xml and gocov)
if [[ "$1" == "badge" ]]; then
    echo -e "${BLUE}Generating coverage badge...${NC}"
    echo ""
    
    # Check if required tools are installed
    if ! command -v gocov &> /dev/null; then
        echo -e "${YELLOW}Installing gocov...${NC}"
        go install github.com/axw/gocov/gocov@latest
    fi
    
    go test -coverprofile=coverage.out ./...
    COVERAGE=$(go tool cover -func=coverage.out | grep total | awk '{print $3}' | sed 's/%//')
    
    echo -e "${GREEN}Coverage: ${COVERAGE}%${NC}"
    
    # Create a simple badge file
    echo "Coverage: ${COVERAGE}%" > coverage_badge.txt
    exit 0
fi

# Show help
echo -e "${BLUE}Usage:${NC}"
echo "  ./run_tests.sh              # Run tests with coverage (default)"
echo "  ./run_tests.sh coverage     # Run tests with coverage"
echo "  ./run_tests.sh coverage html # Run tests and generate HTML report"
echo "  ./run_tests.sh quick        # Quick test run without coverage"
echo "  ./run_tests.sh test <name>  # Run specific test by name"
echo "  ./run_tests.sh race         # Run tests with race detection"
echo "  ./run_tests.sh bench        # Run benchmarks"
echo "  ./run_tests.sh verbose      # Run tests with verbose output"
echo "  ./run_tests.sh failures     # Show only test failures"
echo "  ./run_tests.sh clean        # Clean cache and run tests"
echo "  ./run_tests.sh watch        # Run tests in watch mode"
echo "  ./run_tests.sh badge        # Generate coverage badge"
echo ""
echo -e "${BLUE}Examples:${NC}"
echo "  ./run_tests.sh"
echo "  ./run_tests.sh coverage html"
echo "  ./run_tests.sh test TestExtract"
echo "  ./run_tests.sh quick"