package validation

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestRequiredRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{name: "an empty string is missing", value: "", wantErr: true},
		{name: "a non-empty string is present", value: "x", wantErr: false},
		{name: "a nil interface is missing", value: nil, wantErr: true},
		{name: "an empty slice is missing", value: []int{}, wantErr: true},
		{name: "a populated slice is present", value: []int{1}, wantErr: false},
		{name: "an empty map is missing", value: map[string]int{}, wantErr: true},
		{name: "a nil pointer is missing", value: (*int)(nil), wantErr: true},
		{name: "a non-nil pointer is present", value: new(int), wantErr: false},
		// Zero is a value, not an absence: an int carries no length, so the
		// default arm of isEmpty must return false or every zero-valued number
		// would read as "required field missing".
		{name: "the number zero is present", value: 0, wantErr: false},
		{name: "false is present", value: false, wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := RequiredRule{}.Validate(tc.value)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("RequiredRule.Validate(%#v) error = %v, wantErr %v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestLengthRules(t *testing.T) {
	t.Parallel()

	t.Run("MinLengthRule", func(t *testing.T) {
		t.Parallel()

		rule := MinLengthRule{Min: 3}
		if err := rule.Validate("abc"); err != nil {
			t.Errorf("Validate(\"abc\") = %v, want nil", err)
		}
		if err := rule.Validate("ab"); err == nil {
			t.Error("Validate(\"ab\") = nil, want a length error")
		}
		if err := rule.Validate(42); err == nil {
			t.Error("Validate(42) = nil, want a type error")
		}
	})

	t.Run("MaxLengthRule", func(t *testing.T) {
		t.Parallel()

		rule := MaxLengthRule{Max: 3}
		if err := rule.Validate("abc"); err != nil {
			t.Errorf("Validate(\"abc\") = %v, want nil", err)
		}
		err := rule.Validate("abcd")
		if err == nil {
			t.Fatal("Validate(\"abcd\") = nil, want a length error")
		}
		if !strings.Contains(err.Error(), "3") {
			t.Errorf("error %q should state the maximum", err)
		}
		if err := rule.Validate(42); err == nil {
			t.Error("Validate(42) = nil, want a type error")
		}
	})
}

func TestEmailRule(t *testing.T) {
	t.Parallel()

	rule := EmailRule{}
	for _, valid := range []string{"a@b.co", "first.last+tag@sub.example.com"} {
		if err := rule.Validate(valid); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []string{"", "not-an-email", "a@b", "a b@c.com", "@b.co"} {
		if err := rule.Validate(invalid); err == nil {
			t.Errorf("Validate(%q) = nil, want an email error", invalid)
		}
	}
	if err := rule.Validate(42); err == nil {
		t.Error("Validate(42) = nil, want a type error")
	}
}

func TestPatternRule(t *testing.T) {
	t.Parallel()

	t.Run("uses the custom message when given", func(t *testing.T) {
		t.Parallel()

		rule := PatternRule{Pattern: regexp.MustCompile(`^[a-z]+$`), Message: "lowercase only"}
		err := rule.Validate("ABC")
		if err == nil || err.Error() != "lowercase only" {
			t.Errorf("Validate = %v, want the custom message", err)
		}
	})

	t.Run("falls back to the default message", func(t *testing.T) {
		t.Parallel()

		rule := PatternRule{Pattern: regexp.MustCompile(`^[a-z]+$`)}
		err := rule.Validate("ABC")
		if err == nil || !strings.Contains(err.Error(), "pattern") {
			t.Errorf("Validate = %v, want the default pattern message", err)
		}
	})

	t.Run("accepts a match", func(t *testing.T) {
		t.Parallel()

		rule := PatternRule{Pattern: regexp.MustCompile(`^[a-z]+$`)}
		if err := rule.Validate("abc"); err != nil {
			t.Errorf("Validate(\"abc\") = %v, want nil", err)
		}
	})

	t.Run("rejects a non-string", func(t *testing.T) {
		t.Parallel()

		rule := PatternRule{Pattern: regexp.MustCompile(`.*`)}
		if err := rule.Validate(42); err == nil {
			t.Error("Validate(42) = nil, want a type error")
		}
	})
}

func TestValidatorWithStructTags(t *testing.T) {
	t.Parallel()

	type signUp struct {
		Email string `validate:"required,email"`
		Name  string `validate:"required"`
		Age   int
	}

	v := NewValidator()

	t.Run("passes a valid object", func(t *testing.T) {
		t.Parallel()

		if err := v.Validate(context.Background(), signUp{Email: "a@b.co", Name: "A"}); err != nil {
			t.Errorf("Validate = %v, want nil", err)
		}
	})

	t.Run("collects one error per failing field", func(t *testing.T) {
		t.Parallel()

		err := v.Validate(context.Background(), signUp{})
		if err == nil {
			t.Fatal("Validate = nil, want validation errors")
		}
		errs, ok := err.(ValidationErrors)
		if !ok {
			t.Fatalf("Validate returned %T, want ValidationErrors", err)
		}
		if len(errs) != 2 {
			t.Fatalf("got %d errors (%v), want 2 — one per failing field", len(errs), errs)
		}
		for _, e := range errs {
			if e.Field != "Email" && e.Field != "Name" {
				t.Errorf("unexpected failing field %q", e.Field)
			}
		}
		// The joined message names the fields so a log line is actionable.
		if msg := err.Error(); !strings.Contains(msg, "Email") || !strings.Contains(msg, "Name") {
			t.Errorf("Validate error %q should name both fields", msg)
		}
	})

	t.Run("accepts a pointer object", func(t *testing.T) {
		t.Parallel()

		if err := v.Validate(context.Background(), &signUp{Email: "a@b.co", Name: "A"}); err != nil {
			t.Errorf("Validate(pointer) = %v, want nil", err)
		}
	})
}

func TestValidatorWithAddedRules(t *testing.T) {
	t.Parallel()

	type order struct {
		SKU      string
		Quantity int
	}

	v := NewValidator()
	v.AddRule("SKU", PatternRule{Pattern: regexp.MustCompile(`^[A-Z]{3}-\d+$`), Message: "SKU format is AAA-000"})

	t.Run("passes a valid object", func(t *testing.T) {
		t.Parallel()

		if err := v.Validate(context.Background(), order{SKU: "ABC-123", Quantity: 2}); err != nil {
			t.Errorf("Validate = %v, want nil", err)
		}
	})

	t.Run("reports the added rule's failure on its field", func(t *testing.T) {
		t.Parallel()

		err := v.Validate(context.Background(), order{SKU: "nope"})
		if err == nil {
			t.Fatal("Validate = nil, want a validation error")
		}
		var errs ValidationErrors
		if !errors.As(err, &errs) {
			t.Fatalf("Validate returned %T, want ValidationErrors", err)
		}
		if len(errs) != 1 || errs[0].Field != "SKU" || errs[0].Message != "SKU format is AAA-000" {
			t.Errorf("got %v, want one SKU error with the custom message", errs)
		}
	})

	t.Run("rules apply in registration order", func(t *testing.T) {
		t.Parallel()

		v := NewValidator()
		v.AddRule("SKU", RequiredRule{})
		v.AddRule("SKU", PatternRule{Pattern: regexp.MustCompile(`^[A-Z]{3}-\d+$`)})

		err := v.Validate(context.Background(), order{})
		var errs ValidationErrors
		if !errors.As(err, &errs) {
			t.Fatalf("Validate returned %T, want ValidationErrors", err)
		}
		// Empty fails both rules; both are reported so the caller sees the
		// whole story in one response.
		if len(errs) != 2 {
			t.Errorf("got %d errors (%v), want 2", len(errs), errs)
		}
	})
}

func TestValidationErrorStrings(t *testing.T) {
	t.Parallel()

	single := ValidationError{Field: "Email", Message: "invalid email format"}
	if got, want := single.Error(), "Email: invalid email format"; got != want {
		t.Errorf("ValidationError.Error() = %q, want %q", got, want)
	}

	joined := ValidationErrors{single, {Field: "Name", Message: "field is required"}}
	if got, want := joined.Error(), "Email: invalid email format; Name: field is required"; got != want {
		t.Errorf("ValidationErrors.Error() = %q, want %q", got, want)
	}
}
