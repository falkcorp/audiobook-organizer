#!/bin/bash
# file: scripts/api_examples.sh
# version: 1.1.1
# guid: 7e8f9a0b-1c2d-3e4f-5a6b-7c8d9e0f1011
# last-edited: 2026-10-30

# Convenient API examples for quick testing
# Source this file or run individual examples

API="${API:-http://localhost:8484/api/v1}"

echo "Audiobook Organizer API Examples"
echo "=================================="
echo ""
echo "Set API endpoint with: export API=http://localhost:8484/api/v1"
echo "Current API: $API"
echo ""

# ============================================================================
# Example 1: List all audiobooks
# ============================================================================
example_list_audiobooks() {
    echo "Example: List all audiobooks"
    curl -X GET "$API/audiobooks" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Example 2: List audiobooks with pagination
# ============================================================================
example_list_audiobooks_paginated() {
    echo "Example: List audiobooks with pagination (limit=25, offset=0)"
    curl -X GET "$API/audiobooks?limit=25&offset=0" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Example 3: Search audiobooks
# ============================================================================
example_search_audiobooks() {
    local search_term="${1:-fantasy}"
    echo "Example: Search audiobooks for '$search_term'"
    curl -X GET "$API/audiobooks?search=$search_term" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Example 4: Get single audiobook
# ============================================================================
example_get_audiobook() {
    local book_id="${1:-book-001}"
    echo "Example: Get audiobook with ID: $book_id"
    curl -X GET "$API/audiobooks/$book_id" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Example 5: Update audiobook metadata
# ============================================================================
example_update_audiobook() {
    local book_id="${1:-book-001}"
    echo "Example: Update audiobook metadata for ID: $book_id"
    curl -X PUT "$API/audiobooks/$book_id" \
        -H "Content-Type: application/json" \
        -d '{
            "title": "Updated Title",
            "author": "Updated Author",
            "genre": "Science Fiction",
            "rating": 4.5,
            "tags": ["scifi", "favorite"]
        }'
    echo ""
}

# ============================================================================
# Example 6: Set metadata overrides
# ============================================================================
example_set_metadata_override() {
    local book_id="${1:-book-001}"
    echo "Example: Set metadata overrides for ID: $book_id"
    curl -X POST "$API/audiobooks/$book_id/metadata-override" \
        -H "Content-Type: application/json" \
        -d '{
            "author": "Corrected Author Name",
            "series": "Corrected Series Name",
            "locked": true
        }'
    echo ""
}

# ============================================================================
# Example 7: Organize single audiobook
# ============================================================================
example_organize_audiobook() {
    local book_id="${1:-book-001}"
    echo "Example: Organize audiobook with ID: $book_id (dry run)"
    curl -X POST "$API/audiobooks/$book_id/organize" \
        -H "Content-Type: application/json" \
        -d '{
            "dry_run": true,
            "copy_instead_of_move": false
        }'
    echo ""
}

# ============================================================================
# Example 8: Organize all audiobooks
# ============================================================================
example_organize_all() {
    echo "Example: Organize all audiobooks (dry run)"
    curl -X POST "$API/audiobooks/organize-all" \
        -H "Content-Type: application/json" \
        -d '{
            "dry_run": true,
            "copy_instead_of_move": false,
            "skip_if_exists": true
        }'
    echo ""
}

# ============================================================================
# Example 9: List soft-deleted audiobooks
# ============================================================================
example_list_soft_deleted() {
    echo "Example: List soft-deleted audiobooks"
    curl -X GET "$API/audiobooks/soft-deleted?limit=50&offset=0" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Example 10: List exact-match duplicate candidates
# (GET /audiobooks/duplicates was retired and answers 410 Gone; the Review
# page's Duplicates lane reads this route instead.)
# ============================================================================
example_list_duplicates() {
    echo "Example: List exact-match duplicate book candidates"
    curl -X GET "$API/dedup/candidates?entity_type=book&layer=exact&limit=50&offset=0" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Example 11: Add import path
# ============================================================================
example_add_import_path() {
    local path="${1:-/home/user/audiobooks}"
    echo "Example: Add import path: $path"
    curl -X POST "$API/import/paths" \
        -H "Content-Type: application/json" \
        -d "{
            \"path\": \"$path\",
            \"name\": \"Main Library\"
        }"
    echo ""
}

# ============================================================================
# Example 12: Import single file
# ============================================================================
example_import_file() {
    local file_path="${1:-/home/user/audiobooks/book.m4b}"
    echo "Example: Import file: $file_path"
    curl -X POST "$API/import/file" \
        -H "Content-Type: application/json" \
        -d "{
            \"file_path\": \"$file_path\",
            \"title\": \"Test Book\"
        }"
    echo ""
}

# ============================================================================
# Example 13: Bulk fetch metadata
# ============================================================================
example_bulk_fetch_metadata() {
    local book_ids="${1:-book-001,book-002}"
    echo "Example: Bulk fetch metadata for books: $book_ids"
    # POST /metadata/bulk-fetch is retired (410). Fetch candidates with the
    # library.bulk-metadata-fetch operation; apply them with
    # metadata.batch-apply-cached, which runs every candidate through the apply gate.
    curl -X POST "$API/operations/v2" \
        -H "Content-Type: application/json" \
        -d "{
            \"def_id\": \"library.bulk-metadata-fetch\",
            \"params\": {\"selection\": {\"book_ids\": [\"$book_ids\"]}, \"skip_cached\": false}
        }"
    echo ""
}

# ============================================================================
# Example 14: List authors
# ============================================================================
example_list_authors() {
    echo "Example: List all authors"
    curl -X GET "$API/authors" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Example 15: List series
# ============================================================================
example_list_series() {
    echo "Example: List all series"
    curl -X GET "$API/series" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Example 16: List works
# ============================================================================
example_list_works() {
    echo "Example: List all works"
    curl -X GET "$API/work" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Example 17: Get work statistics
# ============================================================================
example_work_stats() {
    echo "Example: Get work statistics"
    curl -X GET "$API/work/stats" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Example 18: Get system status
# ============================================================================
example_system_status() {
    echo "Example: Get system status"
    curl -X GET "http://localhost:8484/api/v1/system/status" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Example 19: Get system logs
# ============================================================================
example_system_logs() {
    echo "Example: Get system logs"
    curl -X GET "http://localhost:8484/api/v1/logs?limit=50&offset=0" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Example 20: Health check
# ============================================================================
example_health_check() {
    echo "Example: Health check"
    curl -X GET "http://localhost:8484/api/health" \
        -H "Accept: application/json"
    echo ""
}

# ============================================================================
# Help menu
# ============================================================================
show_help() {
    echo "Usage: source $0 and run: example_function_name"
    echo ""
    echo "Available examples:"
    echo ""
    echo "List operations:"
    echo "  example_list_audiobooks                    - List all audiobooks"
    echo "  example_list_audiobooks_paginated          - List with pagination"
    echo "  example_search_audiobooks [search_term]    - Search audiobooks"
    echo "  example_list_soft_deleted                  - List soft-deleted books"
    echo "  example_list_duplicates                    - List exact-match duplicate candidates"
    echo "  example_list_authors                       - List authors"
    echo "  example_list_series                        - List series"
    echo "  example_list_works                         - List works"
    echo ""
    echo "Get operations:"
    echo "  example_get_audiobook [book_id]            - Get single audiobook"
    echo "  example_work_stats                         - Get work statistics"
    echo "  example_system_status                      - Get system status"
    echo "  example_system_logs                        - Get system logs"
    echo "  example_health_check                       - Health check"
    echo ""
    echo "Update operations:"
    echo "  example_update_audiobook [book_id]         - Update metadata"
    echo "  example_set_metadata_override [book_id]    - Set overrides"
    echo ""
    echo "Organization operations:"
    echo "  example_organize_audiobook [book_id]       - Organize single book"
    echo "  example_organize_all                       - Organize all books"
    echo ""
    echo "Import/Metadata operations:"
    echo "  example_add_import_path [path]             - Add import path"
    echo "  example_import_file [file_path]            - Import file"
    echo "  example_bulk_fetch_metadata [book_ids]     - Fetch metadata"
    echo ""
}

# If script is executed directly, show help
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
    show_help
fi
