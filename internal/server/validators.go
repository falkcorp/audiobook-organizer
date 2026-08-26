// file: internal/server/validators.go
// version: 1.0.0
// guid: 9b0c1d2e-3f4a-5b6c-7d8e-9f0a1b2c3d4e

package server

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ValidationError represents a validation error with code
type ValidationError struct {
	Field   string
	Message string
	Code    string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// ValidateTitle validates that a title is non-empty and has reasonable length
func ValidateTitle(title string, minLength int, maxLength int) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return ValidationError{
			Field:   "title",
			Message: "title is required",
			Code:    "TITLE_REQUIRED",
		}
	}
	if minLength > 0 && len(title) < minLength {
		return ValidationError{
			Field:   "title",
			Message: fmt.Sprintf("title must be at least %d characters", minLength),
			Code:    "TITLE_TOO_SHORT",
		}
	}
	if maxLength > 0 && len(title) > maxLength {
		return ValidationError{
			Field:   "title",
			Message: fmt.Sprintf("title must not exceed %d characters", maxLength),
			Code:    "TITLE_TOO_LONG",
		}
	}
	return nil
}

// ValidatePath validates that a path is non-empty and properly formatted
func ValidatePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return ValidationError{
			Field:   "path",
			Message: "path is required",
			Code:    "PATH_REQUIRED",
		}
	}
	if len(path) > 4096 {
		return ValidationError{
			Field:   "path",
			Message: "path is too long",
			Code:    "PATH_TOO_LONG",
		}
	}
	return nil
}

// ValidateID validates that an ID is non-empty and has reasonable format
func ValidateID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ValidationError{
			Field:   "id",
			Message: "id is required",
			Code:    "ID_REQUIRED",
		}
	}
	if len(id) > 256 {
		return ValidationError{
			Field:   "id",
			Message: "id is too long",
			Code:    "ID_TOO_LONG",
		}
	}
	return nil
}

// ValidateEmail validates that a string is a valid email address
func ValidateEmail(email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return ValidationError{
			Field:   "email",
			Message: "email is required",
			Code:    "EMAIL_REQUIRED",
		}
	}

	// Simple email regex (not RFC-compliant but good for basic validation)
	emailRegex := regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	if !emailRegex.MatchString(email) {
		return ValidationError{
			Field:   "email",
			Message: "email format is invalid",
			Code:    "EMAIL_INVALID",
		}
	}
	return nil
}

// ValidateInteger validates that an integer is within acceptable range
func ValidateInteger(value int, fieldName string, minValue int, maxValue int) error {
	if minValue >= 0 && value < minValue {
		return ValidationError{
			Field:   fieldName,
			Message: fmt.Sprintf("%s must be at least %d", fieldName, minValue),
			Code:    fmt.Sprintf("%s_TOO_SMALL", strings.ToUpper(fieldName)),
		}
	}
	if maxValue >= 0 && value > maxValue {
		return ValidationError{
			Field:   fieldName,
			Message: fmt.Sprintf("%s must not exceed %d", fieldName, maxValue),
			Code:    fmt.Sprintf("%s_TOO_LARGE", strings.ToUpper(fieldName)),
		}
	}
	return nil
}

// ValidateSliceLength validates that a slice has a reasonable length
func ValidateSliceLength(slice any, fieldName string, minLength int, maxLength int) error {
	var length int
	switch s := slice.(type) {
	case []string:
		length = len(s)
	case []int:
		length = len(s)
	case []any:
		length = len(s)
	default:
		return ValidationError{
			Field:   fieldName,
			Message: "unsupported slice type",
			Code:    "INVALID_TYPE",
		}
	}

	if minLength > 0 && length < minLength {
		return ValidationError{
			Field:   fieldName,
			Message: fmt.Sprintf("%s must have at least %d items", fieldName, minLength),
			Code:    fmt.Sprintf("%s_TOO_SHORT", strings.ToUpper(fieldName)),
		}
	}
	if maxLength > 0 && length > maxLength {
		return ValidationError{
			Field:   fieldName,
			Message: fmt.Sprintf("%s must not have more than %d items", fieldName, maxLength),
			Code:    fmt.Sprintf("%s_TOO_LONG", strings.ToUpper(fieldName)),
		}
	}
	return nil
}

// ValidateStringInList validates that a string is one of the allowed values
func ValidateStringInList(value string, fieldName string, allowed []string) error {
	value = strings.TrimSpace(value)
	if slices.Contains(allowed, value) {
		return nil
	}
	return ValidationError{
		Field:   fieldName,
		Message: fmt.Sprintf("%s must be one of: %v", fieldName, allowed),
		Code:    fmt.Sprintf("%s_INVALID_VALUE", strings.ToUpper(fieldName)),
	}
}

// ValidateURL validates that a string is a valid URL
func ValidateURL(url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return ValidationError{
			Field:   "url",
			Message: "url is required",
			Code:    "URL_REQUIRED",
		}
	}

	// Simple URL validation (just checks for http/https)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return ValidationError{
			Field:   "url",
			Message: "url must start with http:// or https://",
			Code:    "URL_INVALID",
		}
	}

	if len(url) > 2048 {
		return ValidationError{
			Field:   "url",
			Message: "url is too long",
			Code:    "URL_TOO_LONG",
		}
	}
	return nil
}

// ValidateYearRange validates that a year is within a reasonable range
func ValidateYearRange(year int) error {
	minYear := 1900
	maxYear := 2100
	if year < minYear || year > maxYear {
		return ValidationError{
			Field:   "year",
			Message: fmt.Sprintf("year must be between %d and %d", minYear, maxYear),
			Code:    "YEAR_OUT_OF_RANGE",
		}
	}
	return nil
}

// ValidateDuration validates that a duration is reasonable
func ValidateDuration(duration int64, fieldName string) error {
	if duration < 0 {
		return ValidationError{
			Field:   fieldName,
			Message: fmt.Sprintf("%s cannot be negative", fieldName),
			Code:    "DURATION_NEGATIVE",
		}
	}
	// Max duration: 1000 hours in seconds
	maxDuration := int64(1000 * 3600)
	if duration > maxDuration {
		return ValidationError{
			Field:   fieldName,
			Message: fmt.Sprintf("%s cannot exceed 1000 hours", fieldName),
			Code:    "DURATION_TOO_LONG",
		}
	}
	return nil
}

// ValidateRating validates that a rating is within valid range
func ValidateRating(rating float64) error {
	if rating < 0 || rating > 10 {
		return ValidationError{
			Field:   "rating",
			Message: "rating must be between 0 and 10",
			Code:    "RATING_OUT_OF_RANGE",
		}
	}
	return nil
}

// ValidateGenre validates that a genre is non-empty
func ValidateGenre(genre string) error {
	genre = strings.TrimSpace(genre)
	if genre == "" {
		return ValidationError{
			Field:   "genre",
			Message: "genre is required",
			Code:    "GENRE_REQUIRED",
		}
	}
	if len(genre) > 255 {
		return ValidationError{
			Field:   "genre",
			Message: "genre is too long",
			Code:    "GENRE_TOO_LONG",
		}
	}
	return nil
}

// ValidateLanguage validates that a language code is valid
func ValidateLanguage(lang string) error {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return ValidationError{
			Field:   "language",
			Message: "language is required",
			Code:    "LANGUAGE_REQUIRED",
		}
	}
	// Simple ISO 639-1 code validation
	langRegex := regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
	if !langRegex.MatchString(lang) {
		return ValidationError{
			Field:   "language",
			Message: "language must be a valid ISO 639-1 code",
			Code:    "LANGUAGE_INVALID",
		}
	}
	return nil
}
