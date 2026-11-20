#!/bin/bash

# file: scripts/run_demo_recording.sh
# version: 1.0.0
# Orchestrates the complete demo recording process

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_PORT="${API_PORT:-8484}"
API_URL="https://localhost:${API_PORT}"
OUTPUT_DIR="${OUTPUT_DIR:-${PROJECT_ROOT}/demo_recordings}"
BUILD_DIR="${PROJECT_ROOT}"
DEMO_VIDEO="${OUTPUT_DIR}/audiobook-demo.webm"

# Logging functions
log_info() {
    echo -e "${BLUE}ℹ️  $1${NC}"
}

log_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

log_warn() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

log_error() {
    echo -e "${RED}❌ $1${NC}"
}

# Cleanup function
cleanup() {
    log_info "Cleaning up..."
    if [ ! -z "$API_PID" ]; then
        log_info "Stopping API server (PID: $API_PID)..."
        kill $API_PID 2>/dev/null || true
        wait $API_PID 2>/dev/null || true
        log_success "API server stopped"
    fi
}

# Set trap to cleanup on exit
trap cleanup EXIT

# Main script
main() {
    echo "╔════════════════════════════════════════════════════════════╗"
    echo "║   Audiobook Organizer - Automated Demo Recording          ║"
    echo "╚════════════════════════════════════════════════════════════╝"
    echo ""

    # Step 1: Build the project with embedded frontend
    log_info "Building project with embedded frontend..."
    cd "$BUILD_DIR"
    if ! make build > /dev/null 2>&1; then
        log_error "Build failed"
        exit 1
    fi
    log_success "Project built successfully"
    echo ""

    # Step 2: Start API server
    log_info "Starting API server on port ${API_PORT}..."
    ./audiobook-organizer serve --port "$API_PORT" > /tmp/api_server.log 2>&1 &
    API_PID=$!
    log_success "API server started (PID: $API_PID)"

    # Step 3: Wait for API to be ready
    log_info "Waiting for API server to be ready..."
    max_attempts=30
    attempt=0
    while [ $attempt -lt $max_attempts ]; do
        if curl -sf -k "${API_URL}/health" > /dev/null 2>&1 ||
           curl -sf -k "${API_URL}/api/health" > /dev/null 2>&1; then
            log_success "API server is ready"
            break
        fi
        attempt=$((attempt + 1))
        if [ $attempt -eq $max_attempts ]; then
            log_error "API server did not start in time"
            log_error "Check /tmp/api_server.log for details"
            exit 1
        fi
        sleep 1
    done
    echo ""

    # Step 4: Create output directory
    mkdir -p "$OUTPUT_DIR"
    log_success "Output directory: $OUTPUT_DIR"
    echo ""

    # Step 5: Check for Node.js and dependencies
    log_info "Checking dependencies..."
    if ! command -v node &> /dev/null; then
        log_error "Node.js is not installed"
        exit 1
    fi
    log_success "Node.js found: $(node --version)"

    # Check for required npm packages
    if ! grep -q "playwright" "$BUILD_DIR/package.json" 2>/dev/null; then
        log_warn "Playwright not found in package.json, installing..."
        cd "$BUILD_DIR"
        npm install --save-dev playwright > /dev/null 2>&1
    fi

    if ! grep -q "axios" "$BUILD_DIR/package.json" 2>/dev/null; then
        log_warn "Axios not found in package.json, installing..."
        cd "$BUILD_DIR"
        npm install --save-dev axios > /dev/null 2>&1
    fi
    log_success "Dependencies ready"
    echo ""

    # Step 6: Run the demo recording script
    log_info "Starting demo recording..."
    log_info "This will record the entire workflow as a video"
    echo ""
    log_warn "A browser window will open - DO NOT CLOSE IT until recording is complete"
    echo ""

    cd "$BUILD_DIR"
    if ! API_URL="$API_URL" OUTPUT_DIR="$OUTPUT_DIR" npx node scripts/record_demo.js; then
        log_error "Demo recording failed"
        exit 1
    fi
    echo ""

    # Step 7: Verify output
    log_info "Verifying recording..."
    if [ -f "$DEMO_VIDEO" ]; then
        VIDEO_SIZE=$(du -h "$DEMO_VIDEO" | cut -f1)
        log_success "Demo video created: $DEMO_VIDEO (${VIDEO_SIZE})"
    else
        log_warn "Demo video not found at expected location"
    fi

    # Step 8: Display summary
    echo ""
    echo "╔════════════════════════════════════════════════════════════╗"
    echo "║                    DEMO RECORDING COMPLETE                 ║"
    echo "╚════════════════════════════════════════════════════════════╝"
    echo ""
    log_success "Demo Recording Summary:"
    echo "  📹 Video: ${DEMO_VIDEO}"
    if [ -d "${OUTPUT_DIR}/screenshots" ]; then
        SCREENSHOT_COUNT=$(ls -1 "${OUTPUT_DIR}/screenshots" 2>/dev/null | wc -l)
        echo "  📸 Screenshots: ${SCREENSHOT_COUNT} captured"
    fi
    echo ""
    log_info "Next Steps:"
    echo "  1. Review the video: ${DEMO_VIDEO}"
    echo "  2. Edit the video if needed (add voiceover, captions, etc.)"
    echo "  3. Upload to YouTube or your presentation platform"
    echo "  4. Share with stakeholders"
    echo ""
    log_success "All done! 🎉"
    echo ""
}

# Run main function
main "$@"
