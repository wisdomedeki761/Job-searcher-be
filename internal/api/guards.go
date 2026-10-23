package api

import (
	"net/http"

	"qx-scraper/internal/auth"
	"qx-scraper/internal/model"
	"qx-scraper/internal/store"
)

// currentAdmin loads the authenticated account with FRESH database state, so
// permission changes (can_manage_configs toggles, deletions) take effect on
// the next request even though the JWT itself stays valid.
func (s *Server) currentAdmin(r *http.Request) (*model.Admin, error) {
	claims, ok := auth.ClaimsFrom(r.Context())
	if !ok {
		return nil, store.ErrNotFound
	}
	return s.store.GetAdminByID(r.Context(), claims.AdminID)
}

// requireManage allows only accounts with search-config access through.
func (s *Server) requireManage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin, err := s.currentAdmin(r)
		if err != nil {
			s.writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if !admin.CanManageConfigs {
			s.writeError(w, http.StatusForbidden, "search config access not granted")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireSuper allows only the super admin through (user management).
func (s *Server) requireSuper(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin, err := s.currentAdmin(r)
		if err != nil {
			s.writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if admin.Role != "super_admin" {
			s.writeError(w, http.StatusForbidden, "super admin access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
