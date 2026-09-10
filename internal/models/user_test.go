package models

import "testing"

func TestUserWriteAccessFailsClosed(t *testing.T) {
	for _, role := range []string{RoleAdmin, RoleEditor, RoleViewer, "", "unknown"} {
		for _, disabled := range []bool{false, true} {
			user := User{Role: role, Disabled: disabled}
			want := !disabled && (role == RoleAdmin || role == RoleEditor)
			if user.CanWrite() != want {
				t.Errorf("role=%q disabled=%v: write=%v want=%v", role, disabled, user.CanWrite(), want)
			}
		}
	}
}
