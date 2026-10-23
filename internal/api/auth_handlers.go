package api

import (
	"net/http"
	"strings"

	"qx-scraper/internal/auth"
	"qx-scraper/internal/store"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	Admin     any    `json:"admin"`
}

// handleLogin authenticates an admin and issues a JWT.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := s.decode(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Email == "" || req.Password == "" {
		s.writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	admin, err := s.store.GetAdminByEmail(r.Context(), req.Email)
	if err != nil {
		if err == store.ErrNotFound {
			// Same error for unknown email and wrong password (no user enumeration).
			s.writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	if !auth.CheckPassword(admin.PasswordHash, req.Password) {
		s.writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	pair, err := auth.IssueToken(s.cfg.JWTSecret, s.cfg.JWTExpiry, admin.ID, admin.Email, admin.Role)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "issue token failed")
		return
	}

	_ = s.store.TouchLogin(r.Context(), admin.ID)
	s.writeJSON(w, http.StatusOK, loginResponse{
		Token:     pair.Token,
		ExpiresAt: pair.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
		Admin:     admin,
	})
}

// handleMe returns the currently authenticated admin.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFrom(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	admin, err := s.store.GetAdminByID(r.Context(), claims.AdminID)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "admin not found")
		return
	}
	s.writeJSON(w, http.StatusOK, admin)
}

/* ---------------- account self-service ---------------- */

type updateProfileRequest struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// handleUpdateProfile lets any logged-in user change their own email/name.
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFrom(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req updateProfileRequest
	if err := s.decode(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	email := strings.TrimSpace(strings.ToLower(req.Email))
	if email != "" && !emailRe.MatchString(email) {
		s.writeError(w, http.StatusBadRequest, "invalid email address")
		return
	}

	admin, err := s.store.UpdateAdminProfile(r.Context(), claims.AdminID, email, strings.TrimSpace(req.Name))
	if err == store.ErrEmailTaken {
		s.writeError(w, http.StatusConflict, "an account with this email already exists")
		return
	}
	if err != nil {
		s.writeError(w, errStatus(err), "account not found")
		return
	}
	s.writeJSON(w, http.StatusOK, admin)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangePassword lets any logged-in user change their own password
// (current password required). Existing tokens stay valid until expiry.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFrom(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req changePasswordRequest
	if err := s.decode(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.NewPassword) < 8 {
		s.writeError(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}

	admin, err := s.store.GetAdminByID(r.Context(), claims.AdminID)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "account not found")
		return
	}
	if !auth.CheckPassword(admin.PasswordHash, req.CurrentPassword) {
		s.writeError(w, http.StatusForbidden, "current password is incorrect")
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "password hashing failed")
		return
	}
	if err := s.store.UpdateAdminPassword(r.Context(), claims.AdminID, hash); err != nil {
		s.writeError(w, errStatus(err), "account not found")
		return
	}
	s.log.Info("password changed", "email", admin.Email)
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "password updated"})
}

// handleHealth reports service and database status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	dbOK := true
	if err := s.store.Pool().Ping(r.Context()); err != nil {
		dbOK = false
	}
	status := "ok"
	code := http.StatusOK
	if !dbOK {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}
	s.writeJSON(w, code, map[string]any{
		"status":   status,
		"database": dbOK,
		"ai":       s.ai.Enabled(),
		"sources":  s.sources.Names(),
	})
}
