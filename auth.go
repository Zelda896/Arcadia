package main

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const cookieName = "arcadia_session"

type Auth struct {
	User     string
	Hash     string
	Disabled bool
	sessions *SessionStore
}

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func (a *Auth) Check(user, pw string) bool {
	if a.Disabled || a.Hash == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(user), []byte(a.User)) != 1 {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(a.Hash), []byte(pw)) == nil
}

func (a *Auth) LoggedIn(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return a.sessions.Valid(c.Value)
}

func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    a.sessions.New(),
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
		MaxAge:   14 * 24 * 3600,
	})
}

func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
}

// Wrap 是后台的闸门：没登录的一律弹回登录页。
// 写操作的 CSRF 用 SameSite=Strict cookie 加 Origin 校验挡，个人后台不值得上完整 token 机制。
func (a *Auth) Wrap(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.LoggedIn(r) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			http.Error(w, "跨站请求被拒绝", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true // curl 之类的非浏览器客户端，已经过了 cookie 校验
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return strings.EqualFold(u.Host, host)
}
