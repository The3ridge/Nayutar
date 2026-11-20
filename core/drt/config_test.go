package drt

import (
	"os"
	"testing"
)

func TestConfigFromEnv(t *testing.T) {
	strPtr := func(s string) *string { return &s }

	tests := []struct {
		name    string
		cases   *string
		seed    *string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults when unset",
			want: Config{Cases: 1000, Seed: 1},
		},
		{
			name:  "defaults when empty",
			cases: strPtr(""),
			seed:  strPtr(""),
			want:  Config{Cases: 1000, Seed: 1},
		},
		{
			name:  "explicit values",
			cases: strPtr("10000"),
			seed:  strPtr("42"),
			want:  Config{Cases: 10000, Seed: 42},
		},
		{
			name:    "cases zero",
			cases:   strPtr("0"),
			wantErr: true,
		},
		{
			name:    "cases negative",
			cases:   strPtr("-1"),
			wantErr: true,
		},
		{
			name:    "cases non-numeric",
			cases:   strPtr("abc"),
			wantErr: true,
		},
		{
			name:    "cases float",
			cases:   strPtr("1.5"),
			wantErr: true,
		},
		{
			name:    "seed negative",
			seed:    strPtr("-1"),
			wantErr: true,
		},
		{
			name:    "seed non-numeric",
			seed:    strPtr("abc"),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setOptionalEnv(t, "NAYUTAR_DRT_CASES", tc.cases)
			setOptionalEnv(t, "NAYUTAR_DRT_SEED", tc.seed)

			got, err := ConfigFromEnv()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ConfigFromEnv() = %+v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ConfigFromEnv() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ConfigFromEnv() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func setOptionalEnv(t *testing.T, key string, val *string) {
	t.Helper()
	// Register cleanup restoration with t.Setenv before unsetting when val is nil.
	t.Setenv(key, "")
	if val == nil {
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Setenv(key, *val)
}
