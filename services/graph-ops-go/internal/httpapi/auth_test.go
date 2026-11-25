package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdministratorAuthorization(t *testing.T) {
	for _, role := range []string{"operator", "observer"} {
		t.Run(role, func(t *testing.T) {
			secret := strings.Repeat("ab", 32)
			auth, err := NewAuth(secret, role, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			api := NewWithLab(slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, nil, auth)
			now := time.Now()
			auth.now = func() time.Time { return now }
			request := func(method, path, body, session string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				if session != "" {
					r.Header.Set("Authorization", "Bearer "+session)
				}
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				return w
			}
			for _, v := range []struct{ method, path string }{{"POST", labExecute}, {"POST", labPreview}, {"GET", labOperation}, {"GET", adminHistory}, {"GET", adminDetail}, {"GET", adminMonitoring}, {"GET", metrics}} {
				if rr := request(v.method, v.path, "{}", secret); rr.Code != 401 {
					t.Fatalf("%s: %d", v.path, rr.Code)
				}
			}
			if rr := request("POST", login, `{"token":"wrong"}`, ""); rr.Code != 401 {
				t.Fatal(rr.Code)
			}
			rr := request("POST", login, `{"token":"`+secret+`"}`, "")
			var out struct{ Session string }
			if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &out) != nil || len(out.Session) != 64 || out.Session == secret || rr.Header().Get("Set-Cookie") != "" {
				t.Fatal("invalid login session")
			}
			if rr := request("GET", adminMonitoring, "", out.Session); rr.Code != 200 {
				t.Fatal(rr.Code)
			}
			want := http.StatusServiceUnavailable
			if role == "observer" {
				want = http.StatusForbidden
			}
			if rr := request("POST", labExecute, "{}", out.Session); rr.Code != want {
				t.Fatalf("lab gate = %d want %d", rr.Code, want)
			}
			now = now.Add(time.Minute)
			if rr := request("GET", adminMonitoring, "", out.Session); rr.Code != 401 {
				t.Fatal("expired session accepted")
			}
			if rr := request("POST", preview, `{"current":{},"target":{}}`, ""); rr.Code != 200 {
				t.Fatal("Phase 1 offline preview broken")
			}
		})
	}
}
