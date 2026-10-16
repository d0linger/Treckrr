package store

import (
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

// TestSecurityPolicyDeadline pins who the sign-in policy applies to and from
// when the grace period counts.
func TestSecurityPolicyDeadline(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p := SecurityPolicy{RequireAdmin: true, GraceDays: 7, EnforcedSince: &since}
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		user    models.User
		applies bool
		want    time.Time
	}{
		{"old admin counts from the requirement", models.User{Role: models.RoleAdmin, CreatedAt: old}, true, since.AddDate(0, 0, 7)},
		{"newer admin counts from creation", models.User{Role: models.RoleAdmin, CreatedAt: newer}, true, newer.AddDate(0, 0, 7)},
		{"admin with TOTP", models.User{Role: models.RoleAdmin, TotpEnabled: true}, false, time.Time{}},
		{"admin with passkey", models.User{Role: models.RoleAdmin, HasPasskey: true}, false, time.Time{}},
		{"editor not required", models.User{Role: models.RoleEditor, CreatedAt: old}, false, time.Time{}},
		{"viewer never required", models.User{Role: models.RoleViewer, CreatedAt: old}, false, time.Time{}},
		{"disabled account", models.User{Role: models.RoleAdmin, Disabled: true}, false, time.Time{}},
	} {
		got, applies := p.Deadline(tc.user)
		if applies != tc.applies || !got.Equal(tc.want) {
			t.Errorf("%s: deadline=%v applies=%v, want %v %v", tc.name, got, applies, tc.want, tc.applies)
		}
	}
	p.RequireEditor = true
	if _, applies := p.Deadline(models.User{Role: models.RoleEditor, CreatedAt: old}); !applies {
		t.Error("editor must be required once the policy says so")
	}
}
