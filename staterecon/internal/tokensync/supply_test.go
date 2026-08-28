package tokensync

import "testing"

func TestConvertTotalSupply(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		value     string
		decimals  int
		want      string
		expectErr bool
	}{
		{
			name:     "fractional exact precision",
			value:    "4880.559998718628515833",
			decimals: 18,
			want:     "4880559998718628515833",
		},
		{
			name:     "integer value",
			value:    "1",
			decimals: 6,
			want:     "1000000",
		},
		{
			name:     "leading decimal point",
			value:    ".1",
			decimals: 18,
			want:     "100000000000000000",
		},
		{
			name:     "trim extra zero fractional digits",
			value:    "1.2300",
			decimals: 2,
			want:     "123",
		},
		{
			name:      "reject precision loss",
			value:     "1.001",
			decimals:  2,
			expectErr: true,
		},
		{
			name:      "reject decimals above uint8 range",
			value:     "1",
			decimals:  maxTokenDecimals + 1,
			expectErr: true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := convertTotalSupply(tc.value, tc.decimals)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("convertTotalSupply(%q, %d) error = nil, want error", tc.value, tc.decimals)
				}
				return
			}
			if err != nil {
				t.Fatalf("convertTotalSupply(%q, %d) error = %v", tc.value, tc.decimals, err)
			}
			if got != tc.want {
				t.Fatalf("convertTotalSupply(%q, %d) = %q, want %q", tc.value, tc.decimals, got, tc.want)
			}
		})
	}
}
