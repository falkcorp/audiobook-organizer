<!-- file: internal/books/store/TESTING.md -->
<!-- version: 1.0.1 -->
<!-- guid: 4d5e6f7a-8b9c-0d1e-2f3a-4b5c6d7e8f9a -->
<!-- last-edited: 2026-11-09 -->

# Database Testing Guide

This guide explains how to use the mock database interface for comprehensive
testing of database-dependent code.

## Overview

The `database` package provides:

- **DBInterface**: Interface abstraction for database operations
- **MockDB**: Full-featured mock implementation for testing
- **sqlDBWrapper**: Wrapper to convert `*sql.DB` to `DBInterface`

## Quick Start

### Basic Mock Usage

```go
import (
    "testing"
    database "github.com/jdfalk/audiobook-organizer/internal/books/store"
)

func TestYourFunction(t *testing.T) {
    // Create mock database
    mockDB := database.NewMockDB()

    // Configure mock behavior
    mockDB.ExecFunc = func(query string, args ...interface{}) (sql.Result, error) {
        return &database.MockResult{LastID: 1, AffectedRows: 1}, nil
    }

    mockDB.QueryFunc = func(query string, args ...interface{}) (*sql.Rows, error) {
        // Return test rows
        return nil, nil
    }

    // Use mock in your code
    result, err := mockDB.Exec("INSERT INTO test VALUES (?)", "value")

    // Verify calls
    if mockDB.GetExecCallCount() != 1 {
        t.Error("Expected 1 exec call")
    }
}
```

## Features

### Call Recording

All database operations are automatically recorded:

```go
mockDB := database.NewMockDB()

// Make some calls
mockDB.Query("SELECT * FROM users")
mockDB.Exec("INSERT INTO users VALUES (?)", "john")
mockDB.QueryRow("SELECT * FROM users WHERE id=?", 1)

// Check what was called
fmt.Println("Query calls:", len(mockDB.QueryCalls))
fmt.Println("Exec calls:", len(mockDB.ExecCalls))
fmt.Println("QueryRow calls:", len(mockDB.QueryRowCalls))
```

### Verification Methods

```go
// Verify specific queries were made
err := mockDB.VerifyQuery("SELECT * FROM users")
if err != nil {
    t.Error("Expected query not found")
}

// Get last call details
query, args, err := mockDB.GetLastExec()
fmt.Printf("Last exec: %s with args %v\n", query, args)

// Get call counts
queryCount := mockDB.GetQueryCallCount()
execCount := mockDB.GetExecCallCount()
```

### Error Injection

Test error handling by setting error modes:

```go
mockDB := database.NewMockDB()

// Trigger query errors
mockDB.ErrorMode = "query_error"
_, err := mockDB.Query("SELECT * FROM test")
// err == "mock query error"

// Trigger exec errors
mockDB.ErrorMode = "exec_error"
_, err = mockDB.Exec("INSERT INTO test VALUES (1)")
// err == "mock exec error"

// Available error modes:
// - "query_error"
// - "exec_error"
// - "prepare_error"
// - "begin_error"
// - "close_error"
```

### Custom Behavior

Override any function for specific test scenarios:

```go
mockDB := database.NewMockDB()

// Custom exec behavior
mockDB.ExecFunc = func(query string, args ...interface{}) (sql.Result, error) {
    if strings.Contains(query, "INSERT") {
        return &database.MockResult{LastID: 42, AffectedRows: 1}, nil
    }
    return nil, errors.New("only inserts allowed")
}

// Custom query behavior
mockDB.QueryFunc = func(query string, args ...interface{}) (*sql.Rows, error) {
    // Return test rows based on query
    if strings.Contains(query, "users") {
        return getUserTestRows(), nil
    }
    return nil, sql.ErrNoRows
}
```

### Reset State

Clear all recorded calls between tests:

```go
func TestMultipleScenarios(t *testing.T) {
    mockDB := database.NewMockDB()

    // Test scenario 1
    mockDB.Exec("INSERT INTO test VALUES (1)")
    if mockDB.GetExecCallCount() != 1 {
        t.Error("Expected 1 exec call")
    }

    // Reset for scenario 2
    mockDB.Reset()

    // Now counts are back to zero
    if mockDB.GetExecCallCount() != 0 {
        t.Error("Expected 0 exec calls after reset")
    }
}
```

## Testing Playlist Functions

Example of testing playlist generation with mock database:

```go
func TestGeneratePlaylistsForSeries(t *testing.T) {
    mockDB := database.NewMockDB()

    // Mock series query
    mockDB.QueryFunc = func(query string, args ...interface{}) (*sql.Rows, error) {
        if strings.Contains(query, "SELECT id, name FROM series") {
            // Return test series rows
            return getTestSeriesRows(), nil
        }
        if strings.Contains(query, "SELECT id, title, file_path") {
            // Return test books rows
            return getTestBooksRows(), nil
        }
        return nil, errors.New("unexpected query")
    }

    // Mock playlist insert
    mockDB.ExecFunc = func(query string, args ...interface{}) (sql.Result, error) {
        return &database.MockResult{LastID: 1, AffectedRows: 1}, nil
    }

    // Test the function
    err := GeneratePlaylistsForSeriesWithDB(mockDB)
    if err != nil {
        t.Errorf("Expected no error, got %v", err)
    }

    // Verify correct calls were made
    if mockDB.GetQueryCallCount() < 1 {
        t.Error("Expected at least 1 query")
    }

    err = mockDB.VerifyExec("INSERT INTO playlists")
    if err != nil {
        t.Error("Expected playlist insert")
    }
}
```

## Testing Scanner Functions

Example of testing book insertion with mock database:

```go
func TestSaveBookToDatabase(t *testing.T) {
    mockDB := database.NewMockDB()

    // Mock author lookup
    mockDB.QueryRowFunc = func(query string, args ...interface{}) *sql.Row {
        // Return row with author ID
        return getTestAuthorRow()
    }

    // Mock insert
    mockDB.ExecFunc = func(query string, args ...interface{}) (sql.Result, error) {
        return &database.MockResult{LastID: 123, AffectedRows: 1}, nil
    }

    book := &Book{
        Title: "Test Book",
        Author: "Test Author",
    }

    err := SaveBookToDatabaseWithDB(mockDB, book)
    if err != nil {
        t.Errorf("Expected no error, got %v", err)
    }

    // Verify book insert
    query, args, _ := mockDB.GetLastExec()
    if !strings.Contains(query, "INSERT INTO books") {
        t.Error("Expected book insert query")
    }
}
```

## Thread Safety

MockDB is thread-safe and can be used in concurrent tests:

```go
func TestConcurrentDatabaseAccess(t *testing.T) {
    mockDB := database.NewMockDB()
    mockDB.QueryFunc = func(query string, args ...interface{}) (*sql.Rows, error) {
        return nil, nil
    }

    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func(id int) {
            defer wg.Done()
            mockDB.Query("SELECT ?", id)
            mockDB.Exec("INSERT INTO test VALUES (?)", id)
        }(i)
    }
    wg.Wait()

    // All calls recorded correctly
    if mockDB.GetQueryCallCount() != 100 {
        t.Error("Expected 100 query calls")
    }
}
```

## Best Practices

1. **Always set required Func fields**: Set `ExecFunc`, `QueryFunc`, etc. for
   operations your code uses
2. **Verify calls in tests**: Use `VerifyQuery`, `VerifyExec` to ensure correct
   SQL was executed
3. **Use Reset() between test cases**: Clear state when reusing mocks
4. **Check call counts**: Verify expected number of database operations
5. **Test error paths**: Use `ErrorMode` to test error handling
6. **Keep tests focused**: Mock only what you need for each specific test

## Migration from Global DB

To use DBInterface in existing code, modify functions to accept the interface:

```go
// Before: uses global database.DB
func SaveBook(book *Book) error {
    _, err := database.DB.Exec("INSERT INTO books ...")
    return err
}

// After: accepts DBInterface
func SaveBookWithDB(db database.DBInterface, book *Book) error {
    _, err := db.Exec("INSERT INTO books ...")
    return err
}

// Wrapper for backward compatibility
func SaveBook(book *Book) error {
    return SaveBookWithDB(database.GetDBInterface(), book)
}
```

This allows testing with MockDB while maintaining backward compatibility with
existing code.

## Complete Example

```go
func TestCompletePlaylistGeneration(t *testing.T) {
    // Arrange
    mockDB := database.NewMockDB()

    // Setup series query response
    mockDB.QueryFunc = func(query string, args ...interface{}) (*sql.Rows, error) {
        switch {
        case strings.Contains(query, "series"):
            return getSeriesRows(), nil
        case strings.Contains(query, "books"):
            return getBooksRows(), nil
        default:
            return nil, fmt.Errorf("unexpected query: %s", query)
        }
    }

    // Setup QueryRow for playlist check
    mockDB.QueryRowFunc = func(query string, args ...interface{}) *sql.Row {
        return getNoPlaylistRow()
    }

    // Setup Exec for inserts
    mockDB.ExecFunc = func(query string, args ...interface{}) (sql.Result, error) {
        return &database.MockResult{LastID: 1, AffectedRows: 1}, nil
    }

    // Act
    err := GeneratePlaylistsWithDB(mockDB, "/output")

    // Assert
    if err != nil {
        t.Fatalf("Expected no error, got %v", err)
    }

    // Verify database interactions
    if mockDB.GetQueryCallCount() < 2 {
        t.Error("Expected at least 2 queries (series + books)")
    }

    if mockDB.GetExecCallCount() < 1 {
        t.Error("Expected at least 1 exec (playlist insert)")
    }

    // Verify specific operations
    if err := mockDB.VerifyQuery("SELECT id, name FROM series"); err != nil {
        t.Error("Series query not found")
    }

    if err := mockDB.VerifyExec("INSERT INTO playlists"); err != nil {
        t.Error("Playlist insert not found")
    }
}
```

## Coverage Impact

Using MockDB enables testing of database-dependent code that was previously
untestable:

- **playlist package**: 17.8% → 80%+ (with proper test implementation)
- **scanner package**: 46.2% → 70%+ (testing database save operations)
- **tagger package**: 37.5% → 60%+ (testing tag database operations)
- **database package**: 29.4% → 80%+ (with interface and mock tests)

The mock enables comprehensive testing of error paths, edge cases, and business
logic without requiring a real database or complex test fixtures.
