package admin

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gatusmod/internal/session"
)

const (
	sessionCookie = "gatusmod_session"
	sessionTTL    = session.TTL
	maxFailures   = session.MaxFailures
	minPassword   = 10
)

type ctxKey struct{}

type sessionInfo struct {
	id string
	session.Session
}

func sessionFrom(r *http.Request) (sessionInfo, bool) {
	info, ok := r.Context().Value(ctxKey{}).(sessionInfo)
	return info, ok
}

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: http: https:; style-src 'self'; script-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
}

// protect requires a login session, and for form posts the same origin and a valid CSRF token.
func (a *Admin) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := a.deps.Config()
		if cfg.Admin == nil {
			http.Error(w, "The GatusMOD admin is disabled. Configure gatusmod.admin to enable it.", http.StatusNotFound)
			return
		}
		setSecurityHeaders(w)
		if r.Method == http.MethodPost {
			if !session.SameOrigin(r) {
				http.Error(w, "Cross-site request blocked", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxImportSize)
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				_ = r.ParseMultipartForm(maxImportSize)
			} else {
				_ = r.ParseForm()
			}
		}
		if r.URL.Path == Prefix+"/login" {
			next.ServeHTTP(w, r)
			return
		}
		var info sessionInfo
		if c, err := r.Cookie(sessionCookie); err == nil {
			if sess, ok := a.sessions.Get(c.Value); ok && sess.User == cfg.Admin.Username && sess.Hash == cfg.Admin.PasswordBcrypt {
				info = sessionInfo{id: c.Value, Session: sess}
			}
		}
		if info.id == "" {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, Prefix+"/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			http.Error(w, "Login required", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(info.CSRF)) != 1 {
			http.Error(w, "Invalid or expired form, reload the page and try again", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, info)))
	})
}

func safeNext(next string) string {
	if !strings.HasPrefix(next, Prefix+"/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return Prefix + "/"
	}
	return next
}

func (a *Admin) loginForm(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, ok := a.sessions.Get(c.Value); ok {
			http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
			return
		}
	}
	a.renderLogin(w, r, http.StatusOK, "")
}

func (a *Admin) renderLogin(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	data := map[string]any{"Error": msg, "Next": safeNext(r.FormValue("next"))}
	if err := a.login.ExecuteTemplate(w, "login", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *Admin) loginSubmit(w http.ResponseWriter, r *http.Request) {
	cfg := a.deps.Config()
	ip := session.ClientIP(r)
	if wait := a.sessions.Locked(ip); wait > 0 {
		a.renderLogin(w, r, http.StatusTooManyRequests, "Too many failed attempts. Try again in "+wait.Round(time.Minute).String()+".")
		return
	}
	user, pass := r.PostFormValue("username"), r.PostFormValue("password")
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(cfg.Admin.Username)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(cfg.Admin.PasswordBcrypt), []byte(pass)) == nil
	if !userOK || !passOK {
		a.sessions.Fail(ip)
		a.renderLogin(w, r, http.StatusUnauthorized, "Invalid username or password.")
		return
	}
	a.sessions.Succeed(ip)
	id, _ := a.sessions.Create(cfg.Admin.Username, cfg.Admin.PasswordBcrypt)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: id, Path: Prefix, MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: session.IsHTTPS(r), SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)
}

func (a *Admin) logout(w http.ResponseWriter, r *http.Request) {
	if info, ok := sessionFrom(r); ok {
		a.sessions.Delete(info.id)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: Prefix, MaxAge: -1, HttpOnly: true, Secure: session.IsHTTPS(r), SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, Prefix+"/login", http.StatusSeeOther)
}

func (a *Admin) changePassword(w http.ResponseWriter, r *http.Request) {
	cfg := a.deps.Config()
	info, _ := sessionFrom(r)
	current, next, confirm := r.PostFormValue("current"), r.PostFormValue("new"), r.PostFormValue("confirm")
	msg := ""
	switch {
	case bcrypt.CompareHashAndPassword([]byte(cfg.Admin.PasswordBcrypt), []byte(current)) != nil:
		msg = "The current password is wrong."
	case len(next) < minPassword:
		msg = "The new password must have at least 10 characters."
	case next != confirm:
		msg = "The new passwords do not match."
	}
	if msg != "" {
		a.renderSettings(w, r, map[string]any{"PasswordError": msg})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err == nil {
		err = a.deps.Settings.Update(func(s *settingsT) error {
			s.AdminPasswordBcrypt = string(hash)
			return nil
		})
	}
	if err != nil {
		a.renderSettings(w, r, map[string]any{"PasswordError": "Saving the password failed: " + err.Error()})
		return
	}
	a.sessions.ReplaceAll(info.id, string(hash))
	redirect(w, r, "/settings", "Password changed. Other sessions have been logged out.")
}
