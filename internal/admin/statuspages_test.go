package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestStatusPagesCRUD(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	if body := f.get(t, "/status-pages").Body.String(); !strings.Contains(body, `name="group" value="core"`) || !strings.Contains(body, "No status pages yet") {
		t.Fatalf("the form should offer the groups of Gatus\n%s", body)
	}
	form := url.Values{
		"slug": {"Klant-A"}, "title": {"Klant A"}, "group": {"core"}, "otherGroups": {"later, "},
		"domains": {"https://status.klant.nl/\nstatus.klant.com"},
	}
	if rec := f.post(t, "/status-pages", form, false); rec.Code != http.StatusForbidden {
		t.Errorf("saving without CSRF token should be rejected, got %d", rec.Code)
	}
	if rec := f.post(t, "/status-pages", form, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving failed: %d %s", rec.Code, rec.Body.String())
	}
	p := f.mgr.Get().StatusPageByHost("status.klant.nl")
	if p == nil || p.Slug != "klant-a" || strings.Join(p.Groups, ",") != "core,later" || len(p.Domains) != 2 {
		t.Fatalf("status page not saved: %+v", p)
	}
	body := f.get(t, "/status-pages?statusPage="+p.ID).Body.String()
	if !strings.Contains(body, "https://status.example.com/status/klant-a") || !strings.Contains(body, "Edit status page klant-a") ||
		!strings.Contains(body, `value="core" checked`) || !strings.Contains(body, `name="otherGroups" value="later"`) {
		t.Errorf("edit form not filled\n%s", body)
	}

	form.Set("id", p.ID)
	form.Set("domains", "status.example.com")
	if body := f.post(t, "/status-pages", form, true).Body.String(); !strings.Contains(body, "domain of the base URL") || !strings.Contains(body, "Edit status page") {
		t.Errorf("expected a validation error on the edit form\n%s", body)
	}
	form.Set("domains", "status.klant.nl")
	f.post(t, "/status-pages", form, true)
	if got := f.mgr.Get().StatusPages; len(got) != 1 || len(got[0].Domains) != 1 {
		t.Errorf("editing should replace the page, got %+v", got)
	}

	if rec := f.post(t, "/status-pages/"+p.ID+"/delete", nil, true); rec.Code != http.StatusSeeOther || len(f.mgr.Get().StatusPages) != 0 {
		t.Errorf("deleting failed: %d %+v", rec.Code, f.mgr.Get().StatusPages)
	}
}

func TestStatusPagesPrivate(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	if body := f.get(t, "/status-pages").Body.String(); !strings.Contains(body, `name="endpoint" value="core_api"`) {
		t.Fatalf("the form should offer the endpoints of Gatus\n%s", body)
	}
	form := url.Values{"slug": {"geheim"}, "endpoint": {"core_api"}, "otherEndpoints": {"Later_X"}, "password": {"short"}}
	if body := f.post(t, "/status-pages", form, true).Body.String(); !strings.Contains(body, "at least 10 characters") {
		t.Errorf("a short password should be rejected\n%s", body)
	}
	form.Set("password", "long-enough-password")
	if rec := f.post(t, "/status-pages", form, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving failed: %d %s", rec.Code, rec.Body.String())
	}
	p := f.mgr.Get().StatusPageBySlug("geheim")
	if p == nil || !p.Private() || strings.Join(p.Endpoints, ",") != "core_api,later_x" ||
		bcrypt.CompareHashAndPassword([]byte(p.PasswordBcrypt), []byte("long-enough-password")) != nil {
		t.Fatalf("private status page not saved: %+v", p)
	}
	hash := p.PasswordBcrypt
	body := f.get(t, "/status-pages?statusPage="+p.ID).Body.String()
	if !strings.Contains(body, `value="core_api" checked`) || !strings.Contains(body, `name="otherEndpoints" value="later_x"`) ||
		!strings.Contains(body, "removePassword") || !strings.Contains(body, "<td>Private</td>") {
		t.Errorf("edit form not filled\n%s", body)
	}

	form = url.Values{"id": {p.ID}, "slug": {"geheim"}, "group": {"core"}}
	f.post(t, "/status-pages", form, true)
	if p = f.mgr.Get().StatusPageBySlug("geheim"); p.PasswordBcrypt != hash || len(p.Endpoints) != 0 {
		t.Errorf("an empty password field should keep the password: %+v", p)
	}
	form.Set("removePassword", "on")
	f.post(t, "/status-pages", form, true)
	if p = f.mgr.Get().StatusPageBySlug("geheim"); p.Private() {
		t.Errorf("the password should be removed: %+v", p)
	}
}
