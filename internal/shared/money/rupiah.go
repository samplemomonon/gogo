package money

import "strconv"

func Rupiah(value int64) string {
	prefix := ""
	digits := strconv.FormatInt(value, 10)
	if value < 0 {
		prefix = "-"
		digits = digits[1:]
	}
	return prefix + groupThousands(digits)
}

// groupThousands inserts a dot every three digits, e.g. "1234567" -> "1.234.567".
// It expects a bare digit string with no sign.
func groupThousands(digits string) string {
	n := len(digits)
	if n <= 3 {
		return digits
	}

	first := n % 3
	if first == 0 {
		first = 3
	}

	result := make([]byte, 0, n+(n-first)/3)
	result = append(result, digits[:first]...)
	for i := first; i < n; i += 3 {
		result = append(result, '.')
		result = append(result, digits[i:i+3]...)
	}
	return string(result)
}
