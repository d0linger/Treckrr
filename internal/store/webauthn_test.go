package store

import (
	"errors"
	"testing"
)

// TestCheckedWebauthnSignCount rejects negative and overflowing database counters.
func TestCheckedWebauthnSignCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   int64
		want    uint32
		wantErr bool
	}{
		{name: "zero", value: 0},
		{name: "maximum", value: 1<<32 - 1, want: 1<<32 - 1},
		{name: "negative", value: -1, wantErr: true},
		{name: "above maximum", value: 1 << 32, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checkedWebauthnSignCount(tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkedWebauthnSignCount(%d) error = %v, wantErr %v", tc.value, err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, ErrInvalidWebauthnSignCount) {
				t.Fatalf("error = %v, want ErrInvalidWebauthnSignCount", err)
			}
			if got != tc.want {
				t.Errorf("checkedWebauthnSignCount(%d) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}
