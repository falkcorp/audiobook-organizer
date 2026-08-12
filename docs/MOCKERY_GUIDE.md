<!-- file: docs/MOCKERY_GUIDE.md -->
<!-- version: 1.0.1 -->
<!-- guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890 -->

# Using Mockery to Improve Test Coverage

## Why Mockery Would Help

The server package currently has 66% coverage primarily because testing HTTP
handlers requires:

1. Setting up complex database states
2. Mocking external services (OpenLibrary API, AI parsers)
3. Testing error paths that are hard to trigger with real implementations
4. Isolating handler logic from dependencies

## Current Manual Mocking

The codebase already has some manual mocks:

- `internal/database/mock_store.go` - Manual implementation of Store interface
- Test setup helpers in server tests

**Problems with manual mocks:**

- Time-consuming to maintain
- Easy to get out of sync with interface changes
- Requires updating every time the Store interface changes
- Boilerplate code for return values and call verification

## How Mockery Helps

Mockery auto-generates mocks from interfaces with:

- Automatic method stubs for all interface methods
- Call count tracking
- Argument matching
- Return value configuration
- Expectation-based testing

## Installation

```bash
go install github.com/vektra/mockery/v2@latest
```

## Configuration Example

Create `.mockery.yaml`:

```yaml
# For mockery v2
all: true
dir: '{{.InterfaceDir}}'
filename: 'mock_{{.InterfaceName}}.go'
mockname: 'Mock{{.InterfaceName}}'
outpkg: '{{.PackageName}}'
with-expecter: true

# Generate mocks for specific interfaces
packages:
  github.com/jdfalk/audiobook-organizer/internal/database:
    interfaces:
      Store:
```

## Generating Mocks

```bash
# Generate all mocks
mockery

# Or generate for specific interface
mockery --name Store --dir internal/database
```

## Usage Example

### Before (Manual Mock Setup)

```go
func TestListAudiobooks(t *testing.T) {
    // Complex setup with manual mock
    mockStore := database.NewMockStore()
    mockStore.QueryFunc = func(query string, args ...interface{}) (*sql.Rows, error) {
        // Manual implementation
        return nil, nil
    }

    server := setupServerWithStore(mockStore)
    // ... test code
}
```

### After (Mockery-Generated Mock)

```go
func TestListAudiobooks(t *testing.T) {
    // Clean, simple mock setup
    mockStore := mocks.NewMockStore(t)

    // Set expectations with type-safe methods
    mockStore.EXPECT().
        GetAllBooks().
        Return([]*database.Book{
            {ID: "1", Title: "Test Book"},
        }, nil).
        Once()

    server := setupServerWithStore(mockStore)
    // ... test code

    // Automatic verification that expectations were met
}
```

## Benefits for Server Testing

### 1. Easy Error Path Testing

```go
func TestListAudiobooks_DatabaseError(t *testing.T) {
    mockStore := mocks.NewMockStore(t)

    // Easily inject errors to test error handling
    mockStore.EXPECT().
        GetAllBooks().
        Return(nil, errors.New("database connection lost")).
        Once()

    // Test that server handles error correctly
    // ...
}
```

### 2. Argument Verification

```go
func TestUpdateAudiobook(t *testing.T) {
    mockStore := mocks.NewMockStore(t)

    // Verify exact arguments passed to store
    mockStore.EXPECT().
        UpdateBook(mock.MatchedBy(func(book *database.Book) bool {
            return book.Title == "Expected Title" &&
                   book.AuthorID != nil
        })).
        Return(nil).
        Once()

    // ... test code
}
```

### 3. External Service Mocking

```go
// Create interface for OpenLibrary client
type OpenLibraryClient interface {
    SearchByTitle(title string) (*SearchResult, error)
}

// Generate mock
// mockery --name OpenLibraryClient

func TestFetchMetadata(t *testing.T) {
    mockClient := mocks.NewMockOpenLibraryClient(t)

    mockClient.EXPECT().
        SearchByTitle("The Hobbit").
        Return(&SearchResult{
            Title: "The Hobbit",
            Author: "J.R.R. Tolkien",
        }, nil).
        Once()

    // Test metadata fetching without hitting real API
}
```

## Recommended Implementation Plan

### Phase 1: Generate Core Mocks

```bash
# Database store (most critical)
mockery --name Store --dir internal/database --output internal/database/mocks

# Other key interfaces
mockery --name MetadataService --dir internal/metadata --output internal/metadata/mocks
```

### Phase 2: Update Server Tests

Replace manual mocks with generated mocks in server tests:

- `server_test.go`
- `server_more_test.go`
- `server_coverage_test.go`

### Phase 3: Add Missing Coverage

With easy mocking, add tests for:

- Error handling paths (database errors, validation errors)
- Edge cases (empty results, missing data)
- Complex scenarios (version linking, metadata overrides)

## Expected Coverage Improvement

With mockery-generated mocks, we could reasonably achieve:

- **Server package**: 66% → 85%+ (19% improvement)
- **Overall codebase**: Easier to maintain high coverage
- **Test maintainability**: Significantly improved

## Integration with CI/CD

Add to `Makefile`:

```makefile
.PHONY: mocks
mocks:
	@echo "Generating mocks..."
	mockery

.PHONY: test-coverage
test-coverage: mocks
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out
```

## Alternative: gomock

Another option is `gomock` (from Google):

- Similar functionality to mockery
- Different syntax (uses reflection or code generation)
- Well-established in the Go ecosystem

```bash
go install github.com/golang/mock/mockgen@latest

# Generate mock
mockgen -source=internal/database/store.go -destination=internal/database/mocks/mock_store.go
```

## Conclusion

Mockery would significantly reduce the effort needed to improve server test
coverage by:

1. **Eliminating boilerplate**: No manual mock implementation
2. **Type safety**: Compile-time verification of mock calls
3. **Maintainability**: Auto-regenerate when interfaces change
4. **Better tests**: Easy to test error paths and edge cases
5. **Documentation**: Generated mocks serve as interface documentation

The initial setup takes ~1 hour, but the long-term benefits for test coverage
and maintainability are substantial.
