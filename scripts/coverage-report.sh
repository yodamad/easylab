#!/bin/bash

# Unified Coverage Report Script
# Generates and combines backend (Go) and frontend (Playwright) coverage reports

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
COVERAGE_DIR="$PROJECT_ROOT/coverage"
FRONTEND_COVERAGE_DIR="$COVERAGE_DIR/frontend"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}=== Lab as Code Coverage Report ===${NC}\n"

# Function to check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Function to parse Go coverage percentage
parse_go_coverage() {
    local coverage_file="$1"
    if [ ! -f "$coverage_file" ]; then
        echo "0.0"
        return
    fi
    
    # Extract total coverage percentage
    local coverage=$(go tool cover -func="$coverage_file" 2>/dev/null | grep "total:" | awk '{print $3}' | sed 's/%//')
    if [ -z "$coverage" ]; then
        echo "0.0"
    else
        echo "$coverage"
    fi
}

# Function to parse frontend coverage percentage
parse_frontend_coverage() {
    local summary_file="$FRONTEND_COVERAGE_DIR/coverage-summary.json"
    if [ ! -f "$summary_file" ]; then
        echo "0.0"
        return
    fi
    
    # Extract coverage percentage using grep and awk (works without jq)
    local coverage=$(grep -o '"coveragePercentage":[^,}]*' "$summary_file" | cut -d':' -f2)
    if [ -z "$coverage" ]; then
        echo "0.0"
    else
        echo "$coverage"
    fi
}

# Generate Backend Coverage
echo -e "${YELLOW}Generating backend coverage...${NC}"
cd "$PROJECT_ROOT"

BACKEND_COVERAGE="0.0"
if command_exists go; then
    # Try to generate coverage for internal/server package (most important)
    if go test -coverprofile="$COVERAGE_DIR/coverage.out" -covermode=atomic ./internal/server/... 2>/dev/null; then
        BACKEND_COVERAGE=$(parse_go_coverage "$COVERAGE_DIR/coverage.out")
        echo -e "${GREEN}✓ Backend coverage generated${NC}"
        
        # Generate HTML report
        if [ -f "$COVERAGE_DIR/coverage.out" ]; then
            go tool cover -html="$COVERAGE_DIR/coverage.out" -o "$COVERAGE_DIR/coverage.html" 2>/dev/null || true
        fi
    else
        # Try to use existing coverage file
        if [ -f "$COVERAGE_DIR/coverage.out" ]; then
            BACKEND_COVERAGE=$(parse_go_coverage "$COVERAGE_DIR/coverage.out")
            echo -e "${YELLOW}⚠ Using existing backend coverage file${NC}"
        else
            echo -e "${RED}✗ Could not generate backend coverage${NC}"
        fi
    fi
else
    echo -e "${RED}✗ Go not found, skipping backend coverage${NC}"
fi

# Generate Frontend Coverage
echo -e "\n${YELLOW}Generating frontend coverage...${NC}"

FRONTEND_COVERAGE="0.0"
if command_exists npm && [ -f "$PROJECT_ROOT/package.json" ]; then
    # Check if node_modules exists, if not install dependencies
    if [ ! -d "$PROJECT_ROOT/node_modules" ]; then
        echo "Installing npm dependencies..."
        npm install >/dev/null 2>&1 || true
    fi
    
    # Run tests with coverage collection
    if COLLECT_COVERAGE=true npm run test 2>/dev/null; then
        FRONTEND_COVERAGE=$(parse_frontend_coverage)
        if [ "$FRONTEND_COVERAGE" != "0.0" ]; then
            echo -e "${GREEN}✓ Frontend coverage generated${NC}"
        else
            echo -e "${YELLOW}⚠ Frontend coverage data not found${NC}"
        fi
    else
        echo -e "${YELLOW}⚠ Frontend tests failed or coverage not collected${NC}"
        # Try to read existing coverage if available
        if [ -f "$FRONTEND_COVERAGE_DIR/coverage-summary.json" ]; then
            FRONTEND_COVERAGE=$(parse_frontend_coverage)
            echo -e "${YELLOW}⚠ Using existing frontend coverage data${NC}"
        fi
    fi
else
    echo -e "${RED}✗ npm not found or package.json missing, skipping frontend coverage${NC}"
fi

# Generate Combined Report
echo -e "\n${BLUE}=== Coverage Summary ===${NC}\n"

printf "%-20s %10s\n" "Component" "Coverage"
echo "----------------------------------------"
printf "%-20s %9.2f%%\n" "Backend (Go)" "$BACKEND_COVERAGE"
printf "%-20s %9.2f%%\n" "Frontend (JS)" "$FRONTEND_COVERAGE"

# Calculate overall coverage (simple average)
OVERALL_COVERAGE=$(echo "scale=2; ($BACKEND_COVERAGE + $FRONTEND_COVERAGE) / 2" | bc 2>/dev/null || echo "0.0")
if [ -z "$OVERALL_COVERAGE" ] || [ "$OVERALL_COVERAGE" = "0.0" ]; then
    OVERALL_COVERAGE="0.0"
fi

echo "----------------------------------------"
printf "%-20s %9.2f%%\n" "Overall Average" "$OVERALL_COVERAGE"
echo ""

# Coverage threshold check
COVERAGE_THRESHOLD="${COVERAGE_THRESHOLD:-50}"
BACKEND_THRESHOLD="${BACKEND_THRESHOLD:-50}"
FRONTEND_THRESHOLD="${FRONTEND_THRESHOLD:-50}"

echo -e "${BLUE}=== Coverage Thresholds ===${NC}\n"
printf "%-20s %10s %10s\n" "Component" "Current" "Threshold"
echo "----------------------------------------"
printf "%-20s %9.2f%% %9.2f%%" "Backend" "$BACKEND_COVERAGE" "$BACKEND_THRESHOLD"
if (( $(echo "$BACKEND_COVERAGE >= $BACKEND_THRESHOLD" | bc -l 2>/dev/null || echo "0") )); then
    echo -e " ${GREEN}✓${NC}"
else
    echo -e " ${RED}✗${NC}"
fi

printf "%-20s %9.2f%% %9.2f%%" "Frontend" "$FRONTEND_COVERAGE" "$FRONTEND_THRESHOLD"
if (( $(echo "$FRONTEND_COVERAGE >= $FRONTEND_THRESHOLD" | bc -l 2>/dev/null || echo "0") )); then
    echo -e " ${GREEN}✓${NC}"
else
    echo -e " ${RED}✗${NC}"
fi
echo ""

# Save report to file
REPORT_FILE="$COVERAGE_DIR/coverage-report.txt"
cat > "$REPORT_FILE" <<EOF
Lab as Code Coverage Report
Generated: $(date)

Backend Coverage: ${BACKEND_COVERAGE}%
Frontend Coverage: ${FRONTEND_COVERAGE}%
Overall Average: ${OVERALL_COVERAGE}%

Backend Threshold: ${BACKEND_THRESHOLD}% $(if (( $(echo "$BACKEND_COVERAGE >= $BACKEND_THRESHOLD" | bc -l 2>/dev/null || echo "0") )); then echo "PASS"; else echo "FAIL"; fi)
Frontend Threshold: ${FRONTEND_THRESHOLD}% $(if (( $(echo "$FRONTEND_COVERAGE >= $FRONTEND_THRESHOLD" | bc -l 2>/dev/null || echo "0") )); then echo "PASS"; else echo "FAIL"; fi)

Backend Coverage Report: $COVERAGE_DIR/coverage.html
Frontend Coverage Data: $FRONTEND_COVERAGE_DIR/coverage-summary.json
EOF

echo -e "${GREEN}Coverage report saved to: $REPORT_FILE${NC}"
echo -e "${BLUE}Backend HTML report: $COVERAGE_DIR/coverage.html${NC}"
if [ -f "$FRONTEND_COVERAGE_DIR/coverage-summary.json" ]; then
    echo -e "${BLUE}Frontend coverage summary: $FRONTEND_COVERAGE_DIR/coverage-summary.json${NC}"
fi

# Exit with error if thresholds not met
if (( $(echo "$BACKEND_COVERAGE < $BACKEND_THRESHOLD" | bc -l 2>/dev/null || echo "1") )) || \
   (( $(echo "$FRONTEND_COVERAGE < $FRONTEND_THRESHOLD" | bc -l 2>/dev/null || echo "1") )); then
    exit 1
fi

exit 0

