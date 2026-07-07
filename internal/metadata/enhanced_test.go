// file: internal/metadata/enhanced_test.go
// version: 1.2.0
// guid: 8f7e6d5c-4b3a-2c1d-0e9f-8a7b6c5d4e3f
// last-edited: 2026-07-07

package metadata

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/stretchr/testify/mock"
)

// newMockStore provides a shared mock store for metadata tests.
func newMockStore(t *testing.T) *mocks.MockStore {
	t.Helper()
	return mocks.NewMockStore(t)
}

func TestDefaultValidationRules(t *testing.T) {
	rules := DefaultValidationRules()

	if rules == nil {
		t.Fatal("Expected non-nil validation rules")
	}

	// Check that key rules exist
	if _, ok := rules["title"]; !ok {
		t.Error("Expected title validation rule")
	}
	if _, ok := rules["author"]; !ok {
		t.Error("Expected author validation rule")
	}
	if _, ok := rules["format"]; !ok {
		t.Error("Expected format validation rule")
	}

	// Verify title is required
	titleRule := rules["title"]
	if !titleRule.Required {
		t.Error("Expected title to be required")
	}
	if titleRule.MinLength != 1 {
		t.Errorf("Expected title MinLength=1, got %d", titleRule.MinLength)
	}
	if titleRule.MaxLength != 500 {
		t.Errorf("Expected title MaxLength=500, got %d", titleRule.MaxLength)
	}

	// Verify format has allowed values
	formatRule := rules["format"]
	if len(formatRule.AllowedValues) == 0 {
		t.Error("Expected format to have allowed values")
	}
}

func TestDefaultValidationRules_PublishDateValidator(t *testing.T) {
	rules := DefaultValidationRules()
	rule, ok := rules["publishDate"]
	if !ok {
		t.Fatal("Expected publishDate validation rule")
	}
	if rule.CustomValidator == nil {
		t.Fatal("Expected publishDate custom validator")
	}

	if err := rule.CustomValidator(123); err == nil {
		t.Error("Expected error for non-string publishDate")
	}
	if err := rule.CustomValidator("2024-13-01"); err == nil {
		t.Error("Expected error for invalid publishDate format")
	}
	if err := rule.CustomValidator("2024-01-15"); err != nil {
		t.Errorf("Expected valid publishDate, got error: %v", err)
	}
}

func TestValidateMetadata_RequiredField(t *testing.T) {
	rules := map[string]ValidationRule{
		"title": {
			Field:    "title",
			Required: true,
		},
	}

	// Test missing required field
	updates := map[string]interface{}{}
	errors := ValidateMetadata(updates, rules)
	if len(errors) > 0 {
		t.Error("Expected no error for missing field when not in updates")
	}

	// Test empty required field
	updates = map[string]interface{}{
		"title": "",
	}
	errors = ValidateMetadata(updates, rules)
	if len(errors) != 1 {
		t.Errorf("Expected 1 error for empty required field, got %d", len(errors))
	}

	// Test nil required field
	updates = map[string]interface{}{
		"title": nil,
	}
	errors = ValidateMetadata(updates, rules)
	if len(errors) != 1 {
		t.Errorf("Expected 1 error for nil required field, got %d", len(errors))
	}

	// Test valid required field
	updates = map[string]interface{}{
		"title": "Valid Title",
	}
	errors = ValidateMetadata(updates, rules)
	if len(errors) != 0 {
		t.Errorf("Expected no errors for valid field, got %d", len(errors))
	}
}

func TestValidateMetadata_LengthConstraints(t *testing.T) {
	rules := map[string]ValidationRule{
		"title": {
			Field:     "title",
			MinLength: 5,
			MaxLength: 20,
		},
	}

	// Test too short
	updates := map[string]interface{}{
		"title": "Hi",
	}
	errors := ValidateMetadata(updates, rules)
	if len(errors) != 1 {
		t.Errorf("Expected 1 error for too short, got %d", len(errors))
	}

	// Test too long
	updates = map[string]interface{}{
		"title": "This is a very long title that exceeds the maximum length",
	}
	errors = ValidateMetadata(updates, rules)
	if len(errors) != 1 {
		t.Errorf("Expected 1 error for too long, got %d", len(errors))
	}

	// Test valid length
	updates = map[string]interface{}{
		"title": "Valid Title",
	}
	errors = ValidateMetadata(updates, rules)
	if len(errors) != 0 {
		t.Errorf("Expected no errors for valid length, got %d", len(errors))
	}
}

func TestValidateMetadata_AllowedValues(t *testing.T) {
	rules := map[string]ValidationRule{
		"format": {
			Field:         "format",
			AllowedValues: []string{"m4b", "mp3", "flac"},
		},
	}

	// Test invalid value
	updates := map[string]interface{}{
		"format": "wav",
	}
	errors := ValidateMetadata(updates, rules)
	if len(errors) != 1 {
		t.Errorf("Expected 1 error for invalid value, got %d", len(errors))
	}

	// Test valid value
	updates = map[string]interface{}{
		"format": "m4b",
	}
	errors = ValidateMetadata(updates, rules)
	if len(errors) != 0 {
		t.Errorf("Expected no errors for valid value, got %d", len(errors))
	}

	// Test case insensitive
	updates = map[string]interface{}{
		"format": "M4B",
	}
	errors = ValidateMetadata(updates, rules)
	if len(errors) != 0 {
		t.Errorf("Expected no errors for case insensitive match, got %d", len(errors))
	}
}

func TestValidateMetadata_CustomValidator(t *testing.T) {
	rules := map[string]ValidationRule{
		"publishDate": {
			Field: "publishDate",
			CustomValidator: func(v interface{}) error {
				str, ok := v.(string)
				if !ok {
					return errors.New("must be string")
				}
				_, err := time.Parse("2006-01-02", str)
				return err
			},
		},
	}

	// Test invalid date format
	updates := map[string]interface{}{
		"publishDate": "invalid-date",
	}
	errors := ValidateMetadata(updates, rules)
	if len(errors) != 1 {
		t.Errorf("Expected 1 error for invalid date, got %d", len(errors))
	}

	// Test valid date
	updates = map[string]interface{}{
		"publishDate": "2024-01-15",
	}
	errors = ValidateMetadata(updates, rules)
	if len(errors) != 0 {
		t.Errorf("Expected no errors for valid date, got %d", len(errors))
	}

	// Test non-string value
	updates = map[string]interface{}{
		"publishDate": 12345,
	}
	errors = ValidateMetadata(updates, rules)
	if len(errors) != 1 {
		t.Errorf("Expected 1 error for non-string, got %d", len(errors))
	}
}

func TestBatchUpdateMetadata_Success(t *testing.T) {
	store := newMockStore(t)

	// Create test books
	authorID := 1
	book1 := &database.Book{
		ID:       "book1",
		Title:    "Old Title 1",
		Format:   "mp3",
		AuthorID: &authorID,
	}
	book2 := &database.Book{
		ID:       "book2",
		Title:    "Old Title 2",
		Format:   "m4b",
		AuthorID: &authorID,
	}
	store.EXPECT().GetBookByID("book1").Return(book1, nil).Once()
	store.EXPECT().GetBookByID("book2").Return(book2, nil).Once()
	store.EXPECT().UpdateBook("book1", mock.MatchedBy(func(book *database.Book) bool {
		return book != nil && book.Title == "New Title 1" && book.Format == "m4b"
	})).Return(book1, nil).Once()
	store.EXPECT().UpdateBook("book2", mock.MatchedBy(func(book *database.Book) bool {
		return book != nil && book.Title == "New Title 2"
	})).Return(book2, nil).Once()

	// Create batch updates
	updates := []MetadataUpdate{
		{
			BookID: "book1",
			Updates: map[string]interface{}{
				"title":  "New Title 1",
				"format": "m4b",
			},
		},
		{
			BookID: "book2",
			Updates: map[string]interface{}{
				"title": "New Title 2",
			},
		},
	}

	// Execute batch update
	errs, successCount := BatchUpdateMetadata(updates, store, false)

	// Verify results
	if len(errs) != 0 {
		t.Errorf("Expected no errors, got %d: %v", len(errs), errs)
	}
	if successCount != 2 {
		t.Errorf("Expected 2 successful updates, got %d", successCount)
	}
}

func TestBatchUpdateMetadata_ValidationFailure(t *testing.T) {
	store := newMockStore(t)

	updates := []MetadataUpdate{
		{
			BookID:   "book1",
			Validate: true,
			Updates: map[string]interface{}{
				"title": "", // Empty title should fail validation
			},
		},
	}

	errs, successCount := BatchUpdateMetadata(updates, store, false)

	if len(errs) != 1 {
		t.Errorf("Expected 1 validation error, got %d", len(errs))
	}
	if successCount != 0 {
		t.Errorf("Expected 0 successful updates, got %d", successCount)
	}
}

func TestBatchUpdateMetadata_BookNotFound(t *testing.T) {
	store := newMockStore(t)
	store.EXPECT().GetBookByID("nonexistent").Return(nil, errors.New("book not found")).Once()

	updates := []MetadataUpdate{
		{
			BookID: "nonexistent",
			Updates: map[string]interface{}{
				"title": "New Title",
			},
		},
	}

	errs, successCount := BatchUpdateMetadata(updates, store, false)

	if len(errs) != 1 {
		t.Errorf("Expected 1 error for missing book, got %d", len(errs))
	}
	if successCount != 0 {
		t.Errorf("Expected 0 successful updates, got %d", successCount)
	}
}

func TestBatchUpdateMetadata_UpdateError(t *testing.T) {
	store := newMockStore(t)

	authorID := 1
	book1 := &database.Book{
		ID:       "book1",
		Title:    "Old Title",
		AuthorID: &authorID,
	}
	store.EXPECT().GetBookByID("book1").Return(book1, nil).Once()
	store.EXPECT().UpdateBook("book1", mock.Anything).Return(nil, errors.New("database error")).Once()

	updates := []MetadataUpdate{
		{
			BookID: "book1",
			Updates: map[string]interface{}{
				"title": "New Title",
			},
		},
	}

	errs, successCount := BatchUpdateMetadata(updates, store, false)

	if len(errs) != 1 {
		t.Errorf("Expected 1 error for update failure, got %d", len(errs))
	}
	if successCount != 0 {
		t.Errorf("Expected 0 successful updates, got %d", successCount)
	}
}

func TestRecordMetadataChange(t *testing.T) {
	history := RecordMetadataChange("book123", "title", "Old Title", "New Title", "user123")

	if history == nil {
		t.Fatal("Expected non-nil history record")
	}
	if history.BookID != "book123" {
		t.Errorf("Expected BookID=book123, got %s", history.BookID)
	}
	if history.Field != "title" {
		t.Errorf("Expected Field=title, got %s", history.Field)
	}
	if history.OldValue != "Old Title" {
		t.Errorf("Expected OldValue=Old Title, got %s", history.OldValue)
	}
	if history.NewValue != "New Title" {
		t.Errorf("Expected NewValue=New Title, got %s", history.NewValue)
	}
	if history.UpdatedBy != "user123" {
		t.Errorf("Expected UpdatedBy=user123, got %s", history.UpdatedBy)
	}
	if history.UpdatedAt.IsZero() {
		t.Error("Expected UpdatedAt to be set")
	}
}

func TestGetMetadataHistory(t *testing.T) {
	store := newMockStore(t)

	history, err := GetMetadataHistory("book123", store)

	// Function is not yet implemented, should return error
	if err == nil {
		t.Error("Expected error for unimplemented function")
	}
	if history != nil {
		t.Error("Expected nil history for unimplemented function")
	}
}

func TestExportMetadata(t *testing.T) {
	authorID := 1
	seriesID := 2
	seriesSeq := 3
	duration := 3600

	books := []database.BookCore{
		{
			ID:             "book1",
			Title:          "Book 1",
			Format:         "m4b",
			FilePath:       "/path/to/book1.m4b",
			AuthorID:       &authorID,
			SeriesID:       &seriesID,
			SeriesSequence: &seriesSeq,
			Duration:       &duration,
		},
		{
			ID:       "book2",
			Title:    "Book 2",
			Format:   "mp3",
			FilePath: "/path/to/book2.mp3",
		},
	}

	result, err := ExportMetadata(books)

	if err != nil {
		t.Fatalf("ExportMetadata failed: %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Check count
	count, ok := result["count"].(int)
	if !ok || count != 2 {
		t.Errorf("Expected count=2, got %v", result["count"])
	}

	// Check exported_at
	if _, ok := result["exported_at"].(string); !ok {
		t.Error("Expected exported_at timestamp")
	}

	// Check books data
	booksData, ok := result["books"].([]map[string]interface{})
	if !ok {
		t.Fatal("Expected books array")
	}
	if len(booksData) != 2 {
		t.Errorf("Expected 2 books, got %d", len(booksData))
	}

	// Verify first book
	book1Data := booksData[0]
	if book1Data["id"] != "book1" {
		t.Errorf("Expected book1 id, got %v", book1Data["id"])
	}
	if book1Data["title"] != "Book 1" {
		t.Errorf("Expected Book 1 title, got %v", book1Data["title"])
	}
	if book1Data["format"] != "m4b" {
		t.Errorf("Expected m4b format, got %v", book1Data["format"])
	}
}

func TestImportMetadata_Success(t *testing.T) {
	store := newMockStore(t)
	store.EXPECT().CreateBook(mock.MatchedBy(func(book *database.Book) bool {
		return book != nil && book.Title == "Imported Book 1" && book.Format == "m4b" && book.Duration != nil && *book.Duration == 3600
	})).Return(&database.Book{ID: "book1"}, nil).Once()
	store.EXPECT().CreateBook(mock.MatchedBy(func(book *database.Book) bool {
		return book != nil && book.Title == "Imported Book 2" && book.Format == "mp3"
	})).Return(&database.Book{ID: "book2"}, nil).Once()

	data := map[string]interface{}{
		"books": []interface{}{
			map[string]interface{}{
				"title":    "Imported Book 1",
				"format":   "m4b",
				"duration": float64(3600),
			},
			map[string]interface{}{
				"title":  "Imported Book 2",
				"format": "mp3",
			},
		},
	}

	count, errs := ImportMetadata(data, store, false)

	if len(errs) != 0 {
		t.Errorf("Expected no errors, got %d: %v", len(errs), errs)
	}
	if count != 2 {
		t.Errorf("Expected 2 imported books, got %d", count)
	}
}

func TestImportMetadata_InvalidFormat(t *testing.T) {
	store := newMockStore(t)

	// Missing books field
	data := map[string]interface{}{}

	count, errs := ImportMetadata(data, store, false)

	if len(errs) != 1 {
		t.Errorf("Expected 1 error for invalid format, got %d", len(errs))
	}
	if count != 0 {
		t.Errorf("Expected 0 imported books, got %d", count)
	}

	// Invalid books field type
	data = map[string]interface{}{
		"books": "not an array",
	}

	count, errs = ImportMetadata(data, store, false)

	if len(errs) != 1 {
		t.Errorf("Expected 1 error for invalid books type, got %d", len(errs))
	}
	if count != 0 {
		t.Errorf("Expected 0 imported books, got %d", count)
	}
}

func TestImportMetadata_InvalidBookData(t *testing.T) {
	store := newMockStore(t)

	data := map[string]interface{}{
		"books": []interface{}{
			"not a map", // Invalid book data
		},
	}

	count, errs := ImportMetadata(data, store, false)

	if len(errs) != 1 {
		t.Errorf("Expected 1 error for invalid book data, got %d", len(errs))
	}
	if count != 0 {
		t.Errorf("Expected 0 imported books, got %d", count)
	}
}

func TestImportMetadata_ValidationFailure(t *testing.T) {
	store := newMockStore(t)

	data := map[string]interface{}{
		"books": []interface{}{
			map[string]interface{}{
				"title": "", // Empty title should fail validation
			},
		},
	}

	count, errs := ImportMetadata(data, store, true)

	if len(errs) != 1 {
		t.Errorf("Expected 1 validation error, got %d", len(errs))
	}
	if count != 0 {
		t.Errorf("Expected 0 imported books, got %d", count)
	}
}

func TestImportMetadata_CreateError(t *testing.T) {
	store := newMockStore(t)
	store.EXPECT().CreateBook(mock.Anything).Return(nil, errors.New("database error")).Once()

	data := map[string]interface{}{
		"books": []interface{}{
			map[string]interface{}{
				"title": "Test Book",
			},
		},
	}

	count, errs := ImportMetadata(data, store, false)

	if len(errs) != 1 {
		t.Errorf("Expected 1 error for create failure, got %d", len(errs))
	}
	if count != 0 {
		t.Errorf("Expected 0 imported books, got %d", count)
	}
}

func TestGetStringField(t *testing.T) {
	data := map[string]interface{}{
		"string_field": "test value",
		"int_field":    123,
	}

	// Test valid string field
	if got := getStringField(data, "string_field"); got != "test value" {
		t.Errorf("Expected 'test value', got %q", got)
	}

	// Test non-string field
	if got := getStringField(data, "int_field"); got != "" {
		t.Errorf("Expected empty string for non-string field, got %q", got)
	}

	// Test missing field
	if got := getStringField(data, "missing"); got != "" {
		t.Errorf("Expected empty string for missing field, got %q", got)
	}
}

func TestGetIntField(t *testing.T) {
	data := map[string]interface{}{
		"float_field":  float64(123),
		"int_field":    456,
		"string_field": "not a number",
	}

	// Test float64 field (JSON numbers)
	if got := getIntField(data, "float_field"); got != 123 {
		t.Errorf("Expected 123, got %d", got)
	}

	// Test int field
	if got := getIntField(data, "int_field"); got != 456 {
		t.Errorf("Expected 456, got %d", got)
	}

	// Test non-numeric field
	if got := getIntField(data, "string_field"); got != 0 {
		t.Errorf("Expected 0 for non-numeric field, got %d", got)
	}

	// Test missing field
	if got := getIntField(data, "missing"); got != 0 {
		t.Errorf("Expected 0 for missing field, got %d", got)
	}
}

func TestGetIntPtrField(t *testing.T) {
	data := map[string]interface{}{
		"float_field":  float64(123),
		"int_field":    456,
		"string_field": "not a number",
	}

	// Test float64 field
	got := getIntPtrField(data, "float_field")
	if got == nil {
		t.Fatal("Expected non-nil pointer for float field")
	}
	if *got != 123 {
		t.Errorf("Expected 123, got %d", *got)
	}

	// Test int field
	got = getIntPtrField(data, "int_field")
	if got == nil {
		t.Fatal("Expected non-nil pointer for int field")
	}
	if *got != 456 {
		t.Errorf("Expected 456, got %d", *got)
	}

	// Test non-numeric field
	got = getIntPtrField(data, "string_field")
	if got != nil {
		t.Errorf("Expected nil for non-numeric field, got %v", got)
	}

	// Test missing field
	got = getIntPtrField(data, "missing")
	if got != nil {
		t.Errorf("Expected nil for missing field, got %v", got)
	}
}

func TestWriteMetadataToFile_BackupFailed(t *testing.T) {
	// Create a temporary file in a read-only directory to test backup failure
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.m4b")

	// Create test file
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Make directory read-only to prevent backup creation
	if err := os.Chmod(tmpDir, 0444); err != nil {
		t.Skipf("Cannot change directory permissions: %v", err)
	}
	defer os.Chmod(tmpDir, 0755) // Restore permissions for cleanup

	config := fileops.DefaultConfig()
	metadata := map[string]interface{}{
		"title": "Test",
	}

	err := WriteMetadataToFile(testFile, metadata, config)

	// Should fail due to backup creation failure
	if err == nil {
		t.Error("Expected error when backup creation fails")
	}
}
