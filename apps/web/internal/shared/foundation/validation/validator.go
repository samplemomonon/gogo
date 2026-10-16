// Package validation provides extensible validation patterns for
// domain models and request DTOs.
package validation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"gogo/internal/shared/kernel"
)

var (
	ErrValidationFailed = errors.New("validation failed")
)

// ValidationError represents a validation failure.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// ValidationErrors is a collection of validation errors.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	var messages []string
	for _, err := range e {
		messages = append(messages, err.Error())
	}
	return strings.Join(messages, "; ")
}

// Validator validates objects using rules.
type Validator struct {
	rules map[string][]Rule
}

// NewValidator creates a new validator.
func NewValidator() *Validator {
	return &Validator{
		rules: make(map[string][]Rule),
	}
}

// Rule is a validation rule.
type Rule interface {
	Validate(value interface{}) error
}

// AddRule registers a validation rule for a field.
func (v *Validator) AddRule(field string, rule Rule) {
	v.rules[field] = append(v.rules[field], rule)
}

// Validate validates an object using registered rules.
func (v *Validator) Validate(ctx context.Context, obj interface{}) error {
	var errs ValidationErrors

	val := reflect.ValueOf(obj)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	typ := val.Type()
	for i := 0; i < val.NumField(); i++ {
		field := typ.Field(i)
		fieldName := field.Name
		fieldValue := val.Field(i).Interface()

		if rules, exists := v.rules[fieldName]; exists {
			for _, rule := range rules {
				if err := rule.Validate(fieldValue); err != nil {
					errs = append(errs, ValidationError{
						Field:   fieldName,
						Message: err.Error(),
					})
				}
			}
		}

		// Check struct tags
		if tag := field.Tag.Get("validate"); tag != "" {
			if err := v.validateTag(fieldName, fieldValue, tag); err != nil {
				errs = append(errs, ValidationError{
					Field:   fieldName,
					Message: err.Error(),
				})
			}
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// validateTag validates a field based on struct tag.
func (v *Validator) validateTag(field string, value interface{}, tag string) error {
	tags := strings.Split(tag, ",")
	for _, t := range tags {
		parts := strings.Split(t, "=")
		ruleName := parts[0]

		switch ruleName {
		case "required":
			if isEmpty(value) {
				return errors.New("field is required")
			}
		case "min":
			if len(parts) < 2 {
				continue
			}
			// Parse and validate min length/value
		case "max":
			if len(parts) < 2 {
				continue
			}
			// Parse and validate max length/value
		case "email":
			if str, ok := value.(string); ok {
				if !isValidEmail(str) {
					return errors.New("invalid email format")
				}
			}
		case "url":
			if str, ok := value.(string); ok {
				if !isValidURL(str) {
					return errors.New("invalid URL format")
				}
			}
		}
	}
	return nil
}

// Built-in rules

// RequiredRule validates that a field is not empty.
type RequiredRule struct{}

func (r RequiredRule) Validate(value interface{}) error {
	if isEmpty(value) {
		return errors.New("field is required")
	}
	return nil
}

// MinLengthRule validates minimum string length.
type MinLengthRule struct {
	Min int
}

func (r MinLengthRule) Validate(value interface{}) error {
	str, ok := value.(string)
	if !ok {
		return errors.New("value must be a string")
	}
	if len(str) < r.Min {
		return fmt.Errorf("must be at least %d characters", r.Min)
	}
	return nil
}

// MaxLengthRule validates maximum string length.
type MaxLengthRule struct {
	Max int
}

func (r MaxLengthRule) Validate(value interface{}) error {
	str, ok := value.(string)
	if !ok {
		return errors.New("value must be a string")
	}
	if len(str) > r.Max {
		return fmt.Errorf("must be at most %d characters", r.Max)
	}
	return nil
}

// EmailRule validates email format.
type EmailRule struct{}

func (r EmailRule) Validate(value interface{}) error {
	str, ok := value.(string)
	if !ok {
		return errors.New("value must be a string")
	}
	if !isValidEmail(str) {
		return errors.New("invalid email format")
	}
	return nil
}

// PatternRule validates against a regex pattern.
type PatternRule struct {
	Pattern *regexp.Regexp
	Message string
}

func (r PatternRule) Validate(value interface{}) error {
	str, ok := value.(string)
	if !ok {
		return errors.New("value must be a string")
	}
	if !r.Pattern.MatchString(str) {
		if r.Message != "" {
			return errors.New(r.Message)
		}
		return errors.New("value does not match required pattern")
	}
	return nil
}

// Helper functions

func isEmpty(value interface{}) bool {
	if value == nil {
		return true
	}

	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.String, reflect.Array, reflect.Slice, reflect.Map:
		return v.Len() == 0
	case reflect.Ptr:
		return v.IsNil()
	default:
		return false
	}
}

func isValidEmail(email string) bool {
	pattern := `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`
	re := regexp.MustCompile(pattern)
	return re.MatchString(email)
}

func isValidURL(url string) bool {
	pattern := `^https?://[^\s/$.?#].[^\s]*$`
	re := regexp.MustCompile(pattern)
	return re.MatchString(url)
}

// Ensure Validator implements kernel.Validator.
var _ kernel.Validator = (*Validator)(nil)
