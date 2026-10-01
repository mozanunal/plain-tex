package app

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// adminUserAction loads the target user for an admin action and enforces the two
// invariants every such action shares: the target exists, and an admin never
// locks the instance out of admin access or deletes themselves by surprise.
type adminTarget struct {
	ID      string
	Email   string
	IsAdmin bool
}

func (s *Server) loadAdminActionTarget(w http.ResponseWriter, r *http.Request) (*User, adminTarget, bool) {
	actor := getUserFromContext(r.Context())
	if actor == nil || !actor.IsAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil, adminTarget{}, false
	}

	targetID := strings.TrimSpace(chi.URLParam(r, "userID"))
	var target adminTarget
	var isAdmin int
	err := s.db.QueryRow("SELECT id, email, is_admin FROM users WHERE id = ?", targetID).Scan(&target.ID, &target.Email, &isAdmin)
	if err == sql.ErrNoRows {
		redirectWithMessage(w, r, "/admin/users", "", "User not found")
		return nil, adminTarget{}, false
	}
	if err != nil {
		http.Error(w, "Failed to load user", http.StatusInternalServerError)
		return nil, adminTarget{}, false
	}
	target.IsAdmin = isAdmin == 1
	return actor, target, true
}

func (s *Server) countAdmins() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE is_admin = 1").Scan(&count)
	return count, err
}

// wouldRemoveLastAdmin reports whether removing admin access from, or deleting,
// the target would leave no admins.
func (s *Server) wouldRemoveLastAdmin(target adminTarget) (bool, error) {
	if !target.IsAdmin {
		return false, nil
	}
	count, err := s.countAdmins()
	if err != nil {
		return false, err
	}
	return count <= 1, nil
}

func (s *Server) handleAdminSetUserDisabled(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, target, ok := s.loadAdminActionTarget(w, r)
		if !ok {
			return
		}
		if disabled && target.ID == actor.ID {
			redirectWithMessage(w, r, "/admin/users", "", "You cannot disable your own account")
			return
		}
		if disabled {
			if last, err := s.wouldRemoveLastAdmin(target); err != nil {
				http.Error(w, "Failed to update user", http.StatusInternalServerError)
				return
			} else if last {
				redirectWithMessage(w, r, "/admin/users", "", "Cannot disable the last admin")
				return
			}
		}

		disabledValue := 0
		if disabled {
			disabledValue = 1
		}
		// Bumping the session version signs a disabled user out immediately.
		if _, err := s.db.Exec(
			"UPDATE users SET disabled = ?, session_version = session_version + 1, updated = datetime('now') WHERE id = ?",
			disabledValue, target.ID,
		); err != nil {
			http.Error(w, "Failed to update user", http.StatusInternalServerError)
			return
		}

		message := "Enabled " + target.Email
		if disabled {
			message = "Disabled " + target.Email + " and signed out their sessions"
		}
		redirectWithMessage(w, r, "/admin/users", message, "")
	}
}

func (s *Server) handleAdminSetUserAdmin(makeAdmin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, target, ok := s.loadAdminActionTarget(w, r)
		if !ok {
			return
		}
		if !makeAdmin {
			if last, err := s.wouldRemoveLastAdmin(target); err != nil {
				http.Error(w, "Failed to update user", http.StatusInternalServerError)
				return
			} else if last {
				redirectWithMessage(w, r, "/admin/users", "", "Cannot remove admin from the last admin")
				return
			}
		}

		adminValue := 0
		if makeAdmin {
			adminValue = 1
		}
		if _, err := s.db.Exec(
			"UPDATE users SET is_admin = ?, updated = datetime('now') WHERE id = ?",
			adminValue, target.ID,
		); err != nil {
			http.Error(w, "Failed to update user", http.StatusInternalServerError)
			return
		}

		message := "Granted admin to " + target.Email
		if !makeAdmin {
			message = "Removed admin from " + target.Email
		}
		redirectWithMessage(w, r, "/admin/users", message, "")
	}
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	actor, target, ok := s.loadAdminActionTarget(w, r)
	if !ok {
		return
	}
	if target.ID == actor.ID {
		redirectWithMessage(w, r, "/admin/users", "", "You cannot delete your own account")
		return
	}
	if last, err := s.wouldRemoveLastAdmin(target); err != nil {
		http.Error(w, "Failed to delete user", http.StatusInternalServerError)
		return
	} else if last {
		redirectWithMessage(w, r, "/admin/users", "", "Cannot delete the last admin")
		return
	}

	// A user who still owns projects is kept, because deleting those projects and
	// their files is a heavier, separate decision. Disable the account instead.
	var ownedProjects int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM projects WHERE user_id = ?", target.ID).Scan(&ownedProjects); err != nil {
		http.Error(w, "Failed to delete user", http.StatusInternalServerError)
		return
	}
	if ownedProjects > 0 {
		redirectWithMessage(w, r, "/admin/users", "",
			"That user still owns projects. Reassign or delete those projects first, or disable the account.")
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		http.Error(w, "Failed to delete user", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	for _, stmt := range []string{
		"DELETE FROM project_members WHERE user_id = ?",
		"DELETE FROM user_git_keys WHERE user_id = ?",
		"DELETE FROM users WHERE id = ?",
	} {
		if _, err := tx.Exec(stmt, target.ID); err != nil {
			http.Error(w, "Failed to delete user", http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "Failed to delete user", http.StatusInternalServerError)
		return
	}

	redirectWithMessage(w, r, "/admin/users", "Deleted "+target.Email, "")
}
