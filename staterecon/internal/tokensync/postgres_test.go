package tokensync

import "testing"

func TestNormalizePostgresDSN(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "url with ssl false",
			raw:  "postgresql://blockscout:blockscout_db_password@127.0.0.1:7432/blockscout?ssl=false",
			want: "postgresql://blockscout:blockscout_db_password@127.0.0.1:7432/blockscout?sslmode=disable",
		},
		{
			name: "url with ssl true",
			raw:  "postgres://user:pass@db.internal:5432/app?ssl=true",
			want: "postgres://user:pass@db.internal:5432/app?sslmode=require",
		},
		{
			name: "url preserves existing sslmode",
			raw:  "postgres://user:pass@db.internal:5432/app?ssl=true&sslmode=disable",
			want: "postgres://user:pass@db.internal:5432/app?ssl=true&sslmode=disable",
		},
		{
			name: "non url dsn unchanged",
			raw:  "host=db.internal port=5432 user=blockscout password=secret dbname=blockscout ssl=false",
			want: "host=db.internal port=5432 user=blockscout password=secret dbname=blockscout ssl=false",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizePostgresDSN(tc.raw)
			if err != nil {
				t.Fatalf("normalizePostgresDSN() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("normalizePostgresDSN() = %q, want %q", got, tc.want)
			}
		})
	}
}
