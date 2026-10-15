// Package product contains the core domain models and business rules for products.
package product

import (
	"strings"
	"unicode/utf8"
)

// ValidateSlug checks the slug format constraints.
func ValidateSlug(slug string) error {
	trimmed := strings.TrimSpace(slug)
	if trimmed == "" {
		return NewValidationError("slug", ErrInvalidSlug)
	}
	if utf8.RuneCountInString(trimmed) > MaxSlugLength {
		return NewValidationError("slug", ErrSlugTooLong)
	}
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		isLower := c >= 'a' && c <= 'z'
		isDigit := c >= '0' && c <= '9'
		if isLower || isDigit || c == '-' {
			continue
		}
		return NewValidationError("slug", ErrInvalidSlug)
	}
	if strings.HasPrefix(trimmed, "-") || strings.HasSuffix(trimmed, "-") {
		return NewValidationError("slug", ErrInvalidSlug)
	}
	return nil
}

// ValidateCurrency checks that the currency is a non-empty, well-formed code.
func ValidateCurrency(currency string) error {
	c := strings.ToUpper(strings.TrimSpace(currency))
	if c == "" {
		return ErrInvalidCurrency
	}
	if len(c) != 3 {
		return ErrInvalidCurrency
	}
	for i := 0; i < len(c); i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return ErrInvalidCurrency
		}
	}
	return nil
}

// Slugify converts a name into a URL slug.
func Slugify(name string) string {
	var b strings.Builder
	b.Grow(len(name))

	lastWasHyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastWasHyphen = false
		default:
			if !lastWasHyphen && b.Len() > 0 {
				b.WriteByte('-')
				lastWasHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
