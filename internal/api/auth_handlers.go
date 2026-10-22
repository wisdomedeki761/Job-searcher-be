package api

import (
	"net/http"

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
