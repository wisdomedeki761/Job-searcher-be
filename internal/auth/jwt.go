package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims carried inside the super admin JWT.
type Claims struct {
	AdminID uuid.UUID `json:"admin_id"`
	Email   string    `json:"email"`
	Role    string    `json:"role"`
	jwt.RegisteredClaims
}

// TokenPair is returned on successful login.
type TokenPair struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// IssueToken signs a JWT for the given admin with the configured expiry.
func IssueToken(secret string, expiry time.Duration, adminID uuid.UUID, email, role string) (TokenPair, error) {
	now := time.Now()
	claims := Claims{
		AdminID: adminID,
		Email:   email,
		Role:    role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   adminID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
			Issuer:    "aj-scraper",
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign token: %w", err)
	}
	return TokenPair{Token: signed, ExpiresAt: now.Add(expiry)}, nil
}

// ParseToken validates the signature and expiry of a bearer token.
func ParseToken(secret, token string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, err
	}
	if !parsed.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

type ctxKey int

const claimsKey ctxKey = 1

// WithClaims stores parsed claims on the request context.
func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsKey, c)
}

// ClaimsFrom retrieves claims stored by the middleware.
func ClaimsFrom(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsKey).(*Claims)
	return c, ok
}

// Middleware validates `Authorization: Bearer <jwt>` and injects claims.
// superAdminOnly additionally requires role == "super_admin".
func Middleware(secret string, superAdminOnly bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if header == "" || !strings.HasPrefix(header, "Bearer ") {
				httpError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			claims, err := ParseToken(secret, strings.TrimPrefix(header, "Bearer "))
			if err != nil {
				httpError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			if superAdminOnly && claims.Role != "super_admin" {
				httpError(w, http.StatusForbidden, "super admin access required")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), claims)))
		})
	}
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
