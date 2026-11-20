#!/bin/bash
# file: scripts/e2e_test.sh
# version: 1.0.0
# guid: 6d7e8f9a-0b1c-2d3e-4f5a-6b7c8d9e0f10
# last-edited: 2026-02-04

# End-to-end testing script for audiobook organizer
# Tests basic API functionality and validates responses

set -e

API_BASE="${API_BASE:-http://localhost:8484/api/v1}"
HEALTH_CHECK="${HEALTH_CHECK:-http://localhost:8484/health}"
VERBOSE="${VERBOSE:-false}"

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Test counters
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0

# Helper functions
log_info() {
    if [ "$VERBOSE" = "true" ]; then
        echo -e "${YELLOW}[INFO]${NC} $1"
    fi
}

log_success() {
    echo -e "${GREEN}✓${NC} $1"
    ((TESTS_PASSED++))
}

log_error() {
    echo -e "${RED}✗${NC} $1"
    ((TESTS_FAILED++))
}

test_endpoint() {
    local name="$1"
    local method="$2"
    local endpoint="$3"
    local expected_code="$4"
    local data="${5:-}"

    ((TESTS_RUN++))

    local cmd="curl -s -w '\n%{http_code}' -X $method '$API_BASE$endpoint'"

    if [ -n "$data" ]; then
        cmd="$cmd -H 'Content-Type: application/json' -d '$data'"
    fi

    log_info "Testing: $method $endpoint"

    local response=$(eval "$cmd")
    local http_code=$(echo "$response" | tail -n1)
    local body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "$expected_code" ]; then
        log_success "$name (HTTP $http_code)"
    else
        log_error "$name (expected $expected_code, got $http_code)"
        if [ "$VERBOSE" = "true" ]; then
            echo "Response body: $body"
        fi
    fi
}

test_pagination() {
    local name="$1"
    local endpoint="$2"

    ((TESTS_RUN++))

    log_info "Testing pagination: $endpoint"

    local response=$(curl -s -X GET "$API_BASE$endpoint")

    # Check for required pagination fields
    if echo "$response" | grep -q '"limit"'; then
        log_success "$name - has limit field"
    else
        log_error "$name - missing limit field"
    fi

    if echo "$response" | grep -q '"offset"'; then
        log_success "$name - has offset field"
    else
        log_error "$name - missing offset field"
    fi

    if echo "$response" | grep -q '"items"'; then
        log_success "$name - has items field"
    else
        log_error "$name - missing items field"
    fi

    # Verify pagination bounds
    local limit=$(echo "$response" | grep -o '"limit":[0-9]*' | cut -d: -f2)
    if [ "$limit" -le 1000 ]; then
        log_success "$name - limit <= 1000"
    else
        log_error "$name - limit exceeds 1000 ($limit)"
    fi

    local offset=$(echo "$response" | grep -o '"offset":[0-9]*' | cut -d: -f2)
    if [ "$offset" -ge 0 ]; then
        log_success "$name - offset >= 0"
    else
        log_error "$name - offset is negative ($offset)"
    fi
}

# Main test execution
main() {
    echo "======================================"
    echo "Audiobook Organizer E2E Tests"
    echo "======================================"
    echo "API Base: $API_BASE"
    echo ""

    # Test 1: Server Health
    echo "Test Suite 1: Server Health"
    echo "---"
    test_endpoint "Health Check" "GET" "" 200
    echo ""

    # Test 2: List Audiobooks
    echo "Test Suite 2: List Audiobooks"
    echo "---"
    test_endpoint "List audiobooks (default)" "GET" "/audiobooks" 200
    test_endpoint "List audiobooks (custom limit)" "GET" "/audiobooks?limit=25" 200
    test_endpoint "List audiobooks (with offset)" "GET" "/audiobooks?limit=50&offset=10" 200
    test_endpoint "List audiobooks (with search)" "GET" "/audiobooks?search=test" 200
    echo ""

    # Test 3: Pagination Validation
    echo "Test Suite 3: Pagination Validation"
    echo "---"
    test_pagination "Audiobooks pagination" "/audiobooks?limit=50&offset=0"
    test_pagination "Audiobooks with invalid limit" "/audiobooks?limit=5000"
    test_pagination "Audiobooks with negative offset" "/audiobooks?offset=-5"
    echo ""

    # Test 4: Soft-Deleted Books
    echo "Test Suite 4: Soft-Deleted Books"
    echo "---"
    test_endpoint "List soft-deleted" "GET" "/audiobooks/soft-deleted" 200
    test_endpoint "List soft-deleted (paginated)" "GET" "/audiobooks/soft-deleted?limit=25&offset=0" 200
    echo ""

    # Test 5: Authors and Series
    echo "Test Suite 5: Authors and Series"
    echo "---"
    test_endpoint "List authors" "GET" "/authors" 200
    test_endpoint "List series" "GET" "/series" 200
    echo ""

    # Test 6: Works
    echo "Test Suite 6: Works"
    echo "---"
    test_endpoint "List works" "GET" "/work" 200
    test_endpoint "Work stats" "GET" "/work/stats" 200
    echo ""

    # Results
    echo "======================================"
    echo "Test Results"
    echo "======================================"
    echo "Tests run: $TESTS_RUN"
    echo -e "Passed: ${GREEN}$TESTS_PASSED${NC}"
    echo -e "Failed: ${RED}$TESTS_FAILED${NC}"

    if [ $TESTS_FAILED -eq 0 ]; then
        echo -e "${GREEN}All tests passed!${NC}"
        exit 0
    else
        echo -e "${RED}Some tests failed!${NC}"
        exit 1
    fi
}

# Run tests
main "$@"
