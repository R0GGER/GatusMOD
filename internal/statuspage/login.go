package statuspage

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gatusmod/internal/config"
	"gatusmod/internal/session"
)

const cookieName = "gatusmod_status"

// loggedIn reports whether the visitor has logged in to the private page with its current password.
func (s *Pages) loggedIn(r *http.Request, p *config.StatusPage) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	sess, ok := s.sessions.Get(c.Value)
	return ok && sess.User == p.ID && sess.Hash == p.PasswordBcrypt
}

// requireLogin sends pages of the dashboard to the login form, and refuses everything else.
func (s *Pages) requireLogin(w http.ResponseWriter, r *http.Request, p *config.StatusPage, strict bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead || !(isPage(r.URL.Path) || strings.HasPrefix(r.URL.Path, PathPrefix)) {
		http.Error(w, "Login required", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, loginURL(p, strict, r.URL.RequestURI()), http.StatusSeeOther)
}

func loginURL(p *config.StatusPage, strict bool, next string) string {
	q := url.Values{"next": {next}}
	if !strict {
		q.Set("page", p.Slug)
	}
	return LoginPath + "?" + q.Encode()
}

// home is where a visitor of the page starts: / on its domain, /status/{slug} on the main site.
func home(p *config.StatusPage, strict bool) string {
	if strict {
		return "/"
	}
	return PathPrefix + p.Slug
}

func safeNext(next string, p *config.StatusPage, strict bool) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") || strings.HasPrefix(next, "/mod/") {
		return home(p, strict)
	}
	return next
}

// routeLogin serves the login and logout of a private page and reports whether it did. On a domain the page is
// the page of the domain, on the main site the page named by the page form value.
func (s *Pages) routeLogin(w http.ResponseWriter, r *http.Request, cfg *config.Config, p *config.StatusPage, strict bool) bool {
	if r.URL.Path != LoginPath && r.URL.Path != LogoutPath {
		return false
	}
	if p == nil {
		p = cfg.StatusPageBySlug(r.FormValue("page"))
	}
	if p == nil || !p.Private() {
		http.NotFound(w, r)
		return true
	}
	switch {
	case r.Method == http.MethodPost && !session.SameOrigin(r):
		http.Error(w, "Cross-site request blocked", http.StatusForbidden)
	case r.URL.Path == LogoutPath && r.Method == http.MethodPost:
		s.logout(w, r, p, strict)
	case r.URL.Path == LoginPath && r.Method == http.MethodPost:
		s.loginSubmit(w, r, p, strict)
	case r.URL.Path == LoginPath && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if s.loggedIn(r, p) {
			http.Redirect(w, r, safeNext(r.FormValue("next"), p, strict), http.StatusSeeOther)
			return true
		}
		s.renderLogin(w, r, p, strict, http.StatusOK, "")
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
	return true
}

func (s *Pages) renderLogin(w http.ResponseWriter, r *http.Request, p *config.StatusPage, strict bool, status int, msg string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'none'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	title := p.Title
	if title == "" {
		title = p.Heading
	}
	if title == "" {
		title = p.Slug
	}
	page := ""
	if !strict {
		page = p.Slug
	}
	data := map[string]any{"Title": title, "Error": msg, "Page": page, "Next": safeNext(r.FormValue("next"), p, strict)}
	_ = s.login.ExecuteTemplate(w, "status_login", data)
}

func (s *Pages) loginSubmit(w http.ResponseWriter, r *http.Request, p *config.StatusPage, strict bool) {
	ip := session.ClientIP(r)
	if wait := s.sessions.Locked(ip); wait > 0 {
		s.renderLogin(w, r, p, strict, http.StatusTooManyRequests, "Too many failed attempts. Try again in "+wait.Round(time.Minute).String()+".")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(p.PasswordBcrypt), []byte(r.PostFormValue("password"))) != nil {
		s.sessions.Fail(ip)
		s.renderLogin(w, r, p, strict, http.StatusUnauthorized, "Wrong password.")
		return
	}
	s.sessions.Succeed(ip)
	if c, err := r.Cookie(cookieName); err == nil {
		s.sessions.Delete(c.Value)
	}
	id, _ := s.sessions.Create(p.ID, p.PasswordBcrypt)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: id, Path: "/", MaxAge: int(session.TTL.Seconds()),
		HttpOnly: true, Secure: session.IsHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, safeNext(r.PostFormValue("next"), p, strict), http.StatusSeeOther)
}

func (s *Pages) logout(w http.ResponseWriter, r *http.Request, p *config.StatusPage, strict bool) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: session.IsHTTPS(r), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, home(p, strict), http.StatusSeeOther)
}
