package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

const devJWTSecret = "dev-secret-change-me-dev-secret-change-me"

// jwtMiddleware validates the Bearer token signed by the main Python backend (HS256).
// Skips /healthz so load-balancers and monitoring can probe without a token.
// Reads JWT_SECRET from the environment; falls back to the dev secret when not set.
func jwtMiddleware(next http.Handler) http.Handler {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		secret = devJWTSecret
	}
	key := []byte(secret)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, errBody("missing or invalid Authorization header"))
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")

		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			writeJSON(w, http.StatusUnauthorized, errBody("malformed token"))
			return
		}

		// Verify HMAC-SHA256 signature.
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(parts[0] + "." + parts[1]))
		expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(expected), []byte(parts[2])) {
			writeJSON(w, http.StatusUnauthorized, errBody("invalid token signature"))
			return
		}

		// Decode payload claims.
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errBody("invalid token payload"))
			return
		}
		var claims map[string]any
		if err := json.Unmarshal(payload, &claims); err != nil {
			writeJSON(w, http.StatusUnauthorized, errBody("invalid token claims"))
			return
		}

		// Check expiry.
		if exp, ok := claims["exp"].(float64); ok {
			if time.Now().Unix() > int64(exp) {
				writeJSON(w, http.StatusUnauthorized, errBody("token expired"))
				return
			}
		}

		// Require typ=access so refresh tokens are rejected.
		if typ, _ := claims["typ"].(string); typ != "access" {
			writeJSON(w, http.StatusUnauthorized, errBody("invalid token type"))
			return
		}

		next.ServeHTTP(w, r)
	})
}
