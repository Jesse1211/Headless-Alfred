package auth

import (
	"crypto/subtle"
	"errors"
	"os"
)

// Auth holds the static credentials loaded from environment variables.
type Auth struct {
	User     string
	Password string
	Token    string
}

// FromEnv constructs Auth from ALFRED_USER, ALFRED_PASSWORD, ALFRED_TOKEN.
// All three are required and must be non-empty.
func FromEnv() (Auth, error) {
	a := Auth{
		User:     os.Getenv("ALFRED_USER"),
		Password: os.Getenv("ALFRED_PASSWORD"),
		Token:    os.Getenv("ALFRED_TOKEN"),
	}
	if a.User == "" || a.Password == "" || a.Token == "" {
		return Auth{}, errors.New("ALFRED_USER, ALFRED_PASSWORD, ALFRED_TOKEN must all be set")
	}
	return a, nil
}

// CheckLogin returns the token if user+password match, else "", false.
// Uses constant-time comparison to defeat timing oracles.
func (a Auth) CheckLogin(user, password string) (string, bool) {
	uMatch := subtle.ConstantTimeCompare([]byte(user), []byte(a.User)) == 1
	pMatch := subtle.ConstantTimeCompare([]byte(password), []byte(a.Password)) == 1
	if uMatch && pMatch {
		return a.Token, true
	}
	return "", false
}

// VerifyToken returns true iff token equals the configured token.
// Empty token always fails.
func (a Auth) VerifyToken(token string) bool {
	if token == "" || a.Token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(a.Token)) == 1
}
