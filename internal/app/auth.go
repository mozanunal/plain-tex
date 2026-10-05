package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const userContextKey contextKey = "user"

type User struct {
	ID      string
	Email   string
	Name    string
	IsAdmin bool
}

type Claims struct {
	UserID         string `json:"user_id"`
	Email          string `json:"email"`
	SessionVersion int    `json:"session_version"`
	jwt.RegisteredClaims
}

func hashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

func checkPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

func (s *Server) createToken(userID, email string, sessionVersion int) (string, error) {
	claims := Claims{
		UserID:         userID,
		Email:          email,
		SessionVersion: sessionVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

func (s *Server) parseToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return s.jwtSecret, nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("token")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		claims, err := s.parseToken(cookie.Value)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		var dbUser struct {
			ID             string
			Email          string
			Name           sql.NullString
			IsAdmin        int
			Disabled       int
			SessionVersion int
		}
		err = s.db.QueryRow(
			"SELECT id, email, name, is_admin, disabled, session_version FROM users WHERE id = ?",
			claims.UserID,
		).Scan(&dbUser.ID, &dbUser.Email, &dbUser.Name, &dbUser.IsAdmin, &dbUser.Disabled, &dbUser.SessionVersion)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		// A token stays valid only while it matches the account's current session
		// version, so a password change, reset, or disable logs the user out of
		// every existing session. A disabled account is refused outright.
		if dbUser.Disabled == 1 || dbUser.SessionVersion != claims.SessionVersion {
			clearSessionCookie(w)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		user := &User{
			ID:      dbUser.ID,
			Email:   dbUser.Email,
			IsAdmin: dbUser.IsAdmin == 1,
		}
		if dbUser.Name.Valid {
			user.Name = dbUser.Name.String
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func getUserFromContext(ctx context.Context) *User {
	user, _ := ctx.Value(userContextKey).(*User)
	return user
}

// setSessionCookie issues the auth cookie. Secure is set when the server is told
// it sits behind TLS, so the cookie is never sent over plain HTTP in production
// while local HTTP development still works.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
}

// issueSession looks up the account's current session version and sets the
// cookie, so a freshly issued token already matches the version the middleware
// checks on the next request.
func (s *Server) issueSession(w http.ResponseWriter, userID, email string) error {
	var sessionVersion int
	if err := s.db.QueryRow("SELECT session_version FROM users WHERE id = ?", userID).Scan(&sessionVersion); err != nil {
		return err
	}
	token, err := s.createToken(userID, email, sessionVersion)
	if err != nil {
		return err
	}
	s.setSessionCookie(w, token)
	return nil
}

// bumpSessionVersion invalidates every existing session for a user.
func (s *Server) bumpSessionVersion(userID string) error {
	_, err := s.db.Exec(
		"UPDATE users SET session_version = session_version + 1, updated = datetime('now') WHERE id = ?",
		userID,
	)
	return err
}
