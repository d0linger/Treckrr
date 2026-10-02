package models

import "time"

// Roles control what a user may do.
const (
	RoleAdmin  = "admin"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

// User is an application account. Admins manage other users.
type User struct {
	ID                 int64
	Username           string
	Email              string
	Role               string
	IsAdmin            bool
	MustChangePassword bool
	TotpEnabled        bool
	Disabled           bool
	CreatedAt          time.Time
}

// CanWrite permits only active users with an explicitly recognized write role.
func (u User) CanWrite() bool {
	return !u.Disabled && (u.Role == RoleAdmin || u.Role == RoleEditor)
}

// RoleLabel returns a German label for the user's role.
func (u User) RoleLabel() string {
	switch u.Role {
	case RoleAdmin:
		return "Administrator"
	case RoleViewer:
		return "Nur-Lesen"
	default:
		return "Erfasser"
	}
}

// Session is an active login session (for the management view).
type Session struct {
	Token     string
	UserID    int64
	UserAgent string
	IP        string
	LastSeen  time.Time
	Created   time.Time
	ExpiresAt time.Time
	Current   bool
}
