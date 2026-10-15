package money

import "testing"

func TestRupiah(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value int64
		want  string
	}{
		{name: "zero", value: 0, want: "0"},
		{name: "single digit", value: 7, want: "7"},
		{name: "hundred", value: 100, want: "100"},
		{name: "under a thousand", value: 999, want: "999"},
		{name: "exactly a thousand", value: 1000, want: "1.000"},
		{name: "ten thousand five hundred", value: 10500, want: "10.500"},
		{name: "millions", value: 1234567, want: "1.234.567"},
		{name: "billions", value: 1000000000, want: "1.000.000.000"},
		{name: "negative hundreds", value: -15000, want: "-15.000"},
		{name: "negative small", value: -999, want: "-999"},
		{name: "negative bounded", value: -12345678, want: "-12.345.678"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := Rupiah(tt.value); got != tt.want {
				t.Errorf("Rupiah(%d) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
