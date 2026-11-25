package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"learning-guide/graph-ops/internal/importer"
)

const login = "/api/v1/admin/login"
const logout = "/api/v1/admin/logout"

const (
	permPreview = 1 << iota
	permWrite
	permHistory
	permMetrics
)

type Auth struct {
	digest   [32]byte
	role     string
	perms    int
	ttl      time.Duration
	now      func() time.Time
	mu       sync.Mutex
	sessions map[[32]byte]time.Time
}

func NewAuth(secret, role string, ttl time.Duration) (*Auth, error) {
	key, err := hex.DecodeString(secret)
	if err != nil || len(key) != 32 || ttl < time.Second || ttl > time.Hour {
		return nil, errors.New("invalid administrator configuration")
	}
	a := &Auth{digest: sha256.Sum256([]byte(secret)), role: role, ttl: ttl, now: time.Now, sessions: make(map[[32]byte]time.Time)}
	switch role {
	case "operator":
		a.perms = permPreview | permWrite | permHistory | permMetrics
	case "observer":
		a.perms = permHistory | permMetrics
	default:
		return nil, errors.New("invalid administrator role")
	}
	return a, nil
}

func (a *Auth) issue(secret string) (string, time.Time, int) {
	got := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(got[:], a.digest[:]) != 1 {
		return "", time.Time{}, 401
	}
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", time.Time{}, 500
	}
	token := hex.EncodeToString(data)
	now := a.now()
	expires := now.Add(a.ttl)
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, v := range a.sessions {
		if !now.Before(v) {
			delete(a.sessions, k)
		}
	}
	if len(a.sessions) >= 64 {
		return "", time.Time{}, 429
	}
	a.sessions[sha256.Sum256([]byte(token))] = expires
	return token, expires, 200
}

func (a *Auth) check(r *http.Request, perm int) int {
	if a == nil {
		return 401
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || len(token) != 64 {
		return 401
	}
	key := sha256.Sum256([]byte(token))
	a.mu.Lock()
	defer a.mu.Unlock()
	expires, ok := a.sessions[key]
	if !ok || !a.now().Before(expires) {
		delete(a.sessions, key)
		return 401
	}
	if a.perms&perm != perm {
		return 403
	}
	return 200
}

func (a *Auth) revoke(r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sha256.Sum256([]byte(token)))
}

func (a *API) processLogin(r *http.Request, body []byte) response {
	if a.auth == nil {
		return failure(503, "admin_unconfigured", "auth", "administrator access is disabled")
	}
	var in struct {
		Token string `json:"token"`
	}
	if issue := importer.DecodeJSON(r.Context(), body, &in); issue != nil {
		return failure(400, issue.Code, "auth", "invalid login request")
	}
	token, expires, status := a.auth.issue(in.Token)
	if status != 200 {
		return failure(status, "login_rejected", "auth", "administrator session could not be created")
	}
	return response{200, "", map[string]any{"session": token, "expires_at": expires, "role": a.auth.role}}
}

func permission(route string) int {
	switch route {
	case labPreview, adminImport:
		return permPreview
	case labExecute:
		return permWrite
	case labOperation, adminHistory, adminDetail:
		return permHistory
	case metrics, adminMonitoring:
		return permMetrics
	default:
		return 0
	}
}

func authDenied(status int) response {
	if status == 403 {
		return failure(403, "permission_denied", "auth", "this session lacks permission")
	}
	return failure(401, "authentication_required", "auth", "valid administrator session required")
}
