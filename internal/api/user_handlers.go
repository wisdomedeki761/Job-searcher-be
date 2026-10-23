package api

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"qx-scraper/internal/auth"
	"qx-scraper/internal/model"
	"qx-scraper/internal/store"
)

// emailRe is a deliberately permissive address check (the mail server is the
// real validator); it just rejects obvious typos like "foo" or "a@b".
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

/* ---------------- request bodies ---------------- */

type createUserRequest struct {
	Email            string `json:"email"`
	Name             string `json:"name"`
	CanManageConfigs bool   `json:"can_manage_configs"`
}

type updateUserRequest struct {
	Email            string `json:"email"`
	Name             string `json:"name"`
	CanManageConfigs *bool  `json:"can_manage_configs"`
}

/* ---------------- responses ---------------- */

// credentialsResponse is returned when an account is created or its password
// is reset. The generated password is included ONLY when it could not be
// emailed (SMTP disabled/failed) so the super admin can share it manually.
type credentialsResponse struct {
	Admin    *model.Admin `json:"admin"`
	Emailed  bool         `json:"emailed"`
	Password string       `json:"password,omitempty"`
	Message  string       `json:"message"`
}

// deliverCredentials hashes and stores hash, then emails the plaintext to the
// account. On mail failure the plaintext rides along in the response.
func (s *Server) deliverCredentials(w http.ResponseWriter, r *http.Request, admin *model.Admin, plain string) {
	resp := credentialsResponse{Admin: admin, Emailed: false}
	if s.mailer.Enabled() {
		if err := s.mailer.SendWelcome(admin.Email, plain, s.cfg.AppURL); err != nil {
			s.log.Error("welcome email failed", "to", admin.Email, "err", err)
			resp.Message = "Account saved, but the email could not be sent — share the password below manually."
		} else {
			resp.Emailed = true
			resp.Message = "Login details emailed to " + admin.Email + "."
		}
	} else {
		resp.Message = "SMTP is not configured — share the password below with the user manually."
	}
	if !resp.Emailed {
		resp.Password = plain
	}
	s.writeJSON(w, http.StatusOK, resp)
}

/* ---------------- super admin: user management ---------------- */

// handleListUsers returns every account (super admin only).
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListAdmins(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "list users failed")
		return
	}
	s.writeJSON(w, http.StatusOK, users)
}

// handleCreateUser creates an account with a generated password and emails
// the credentials to it (super admin only).
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := s.decode(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if !emailRe.MatchString(req.Email) {
		s.writeError(w, http.StatusBadRequest, "a valid email address is required")
		return
	}

	plain, err := auth.GeneratePassword(14)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "password generation failed")
		return
	}
	hash, err := auth.HashPassword(plain)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "password hashing failed")
		return
	}

	// Created users get the plain "user" role; permission comes from the flag.
	admin, err := s.store.CreateAdmin(r.Context(), req.Email, hash, strings.TrimSpace(req.Name), "user", req.CanManageConfigs)
	if err == store.ErrEmailTaken {
		s.writeError(w, http.StatusConflict, "an account with this email already exists")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "create user failed")
		return
	}
	s.log.Info("user created", "email", admin.Email, "id", admin.ID, "can_manage_configs", admin.CanManageConfigs)
	s.deliverCredentials(w, r, admin, plain)
}

// handleGetUser returns one account (super admin only).
func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	admin, err := s.store.GetAdminByID(r.Context(), id)
	if err != nil {
		s.writeError(w, errStatus(err), "user not found")
		return
	}
	s.writeJSON(w, http.StatusOK, admin)
}

// handleUpdateUser edits another account's email, name or permissions.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var req updateUserRequest
	if err := s.decode(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if strings.TrimSpace(req.Email) != "" && !emailRe.MatchString(strings.TrimSpace(req.Email)) {
		s.writeError(w, http.StatusBadRequest, "invalid email address")
		return
	}

	admin, err := s.store.UpdateAdminProfile(r.Context(), id,
		strings.TrimSpace(strings.ToLower(req.Email)), strings.TrimSpace(req.Name))
	if err == store.ErrEmailTaken {
		s.writeError(w, http.StatusConflict, "an account with this email already exists")
		return
	}
	if err != nil {
		s.writeError(w, errStatus(err), "user not found")
		return
	}
	if req.CanManageConfigs != nil {
		if err := s.store.UpdateAdminPermissions(r.Context(), id, *req.CanManageConfigs); err != nil {
			s.writeError(w, errStatus(err), "user not found")
			return
		}
		admin.CanManageConfigs = *req.CanManageConfigs
	}
	s.writeJSON(w, http.StatusOK, admin)
}

// handleDeleteUser removes an account (super admin only). Self-deletion and
// deleting the last super admin are blocked.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	target, err := s.store.GetAdminByID(r.Context(), id)
	if err != nil {
		s.writeError(w, errStatus(err), "user not found")
		return
	}
	me, err := s.currentAdmin(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if target.ID == me.ID {
		s.writeError(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	if target.Role == "super_admin" {
		n, err := s.store.CountSuperAdmins(r.Context())
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "count super admins failed")
			return
		}
		if n <= 1 {
			s.writeError(w, http.StatusBadRequest, "the last super admin cannot be deleted")
			return
		}
	}
	if err := s.store.DeleteAdmin(r.Context(), id); err != nil {
		s.writeError(w, errStatus(err), "user not found")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleResetUserPassword generates a fresh password for an account and
// emails it (super admin only).
func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	admin, err := s.store.GetAdminByID(r.Context(), id)
	if err != nil {
		s.writeError(w, errStatus(err), "user not found")
		return
	}

	plain, err := auth.GeneratePassword(14)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "password generation failed")
		return
	}
	hash, err := auth.HashPassword(plain)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "password hashing failed")
		return
	}
	if err := s.store.UpdateAdminPassword(r.Context(), id, hash); err != nil {
		s.writeError(w, errStatus(err), "user not found")
		return
	}
	s.log.Info("password reset", "email", admin.Email, "by", "super_admin")
	s.deliverCredentials(w, r, admin, plain)
}
