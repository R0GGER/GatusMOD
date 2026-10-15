package admin

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"gatusmod/internal/badge"
	"gatusmod/internal/config"
	"gatusmod/internal/settings"
)

// statusPageForm holds the add/edit form of a status page; Domains has one domain per line.
type statusPageForm struct {
	ID, Slug, Title, Heading, Subheading, Domains, OtherGroups, OtherEndpoints string
	Groups                                                                     []groupOption
	Endpoints                                                                  []endpointGroup
	Private                                                                    bool
}

type groupOption struct {
	Name    string
	Checked bool
}

type endpointGroup struct {
	Group     string
	Endpoints []endpointOption
	// Open is set when an endpoint of the group is checked, so the choice is visible.
	Open bool
}

type endpointOption struct {
	Key, Name string
	Checked   bool
}

type statusPageRow struct {
	config.StatusPage
	URL string
}

// knownEndpoints are the Gatus endpoints, falling back to the imports when Gatus cannot be read.
func (a *Admin) knownEndpoints(r *http.Request) []badge.Endpoint {
	endpoints, _, _ := badge.List(r.Context(), a.deps.Client, a.deps.Config(), a.deps.Syncer.Records())
	return endpoints
}

func groupsOf(endpoints []badge.Endpoint) []string {
	seen := map[string]bool{}
	var groups []string
	for _, ep := range endpoints {
		if ep.Group != "" && !seen[strings.ToLower(ep.Group)] {
			seen[strings.ToLower(ep.Group)] = true
			groups = append(groups, ep.Group)
		}
	}
	sort.Strings(groups)
	return groups
}

// newStatusPageForm shows every known group and endpoint, with those of the page checked; groups and endpoints
// Gatus does not know (yet) are kept in OtherGroups and OtherEndpoints.
func newStatusPageForm(p config.StatusPage, endpoints []badge.Endpoint) statusPageForm {
	f := statusPageForm{ID: p.ID, Slug: p.Slug, Title: p.Title, Heading: p.Heading, Subheading: p.Subheading,
		Domains: strings.Join(p.Domains, "\n"), Private: p.Private()}
	known := groupsOf(endpoints)
	var other []string
	for _, g := range p.Groups {
		found := false
		for _, k := range known {
			found = found || strings.EqualFold(k, g)
		}
		if !found {
			other = append(other, g)
		}
	}
	for _, k := range known {
		f.Groups = append(f.Groups, groupOption{Name: k, Checked: p.HasGroup(k)})
	}
	f.OtherGroups = strings.Join(other, ", ")

	keys := map[string]bool{}
	for _, ep := range endpoints {
		keys[ep.Key] = true
		if n := len(f.Endpoints); n == 0 || f.Endpoints[n-1].Group != ep.Group {
			f.Endpoints = append(f.Endpoints, endpointGroup{Group: ep.Group})
		}
		g := &f.Endpoints[len(f.Endpoints)-1]
		checked := p.HasEndpoint(ep.Key)
		g.Endpoints = append(g.Endpoints, endpointOption{Key: ep.Key, Name: ep.Name, Checked: checked})
		g.Open = g.Open || checked
	}
	var otherKeys []string
	for _, k := range p.Endpoints {
		if !keys[k] {
			otherKeys = append(otherKeys, k)
		}
	}
	f.OtherEndpoints = strings.Join(otherKeys, ", ")
	return f
}

func statusPageFromForm(r *http.Request) config.StatusPage {
	v := func(key string) string { return strings.TrimSpace(r.PostFormValue(key)) }
	p := config.StatusPage{ID: v("id"), Slug: v("slug"), Title: v("title"), Heading: v("heading"), Subheading: v("subheading")}
	p.Domains = strings.Fields(strings.ReplaceAll(r.PostFormValue("domains"), ",", " "))
	p.Groups = append(p.Groups, r.PostForm["group"]...)
	p.Groups = append(p.Groups, strings.Split(r.PostFormValue("otherGroups"), ",")...)
	p.Endpoints = append(p.Endpoints, r.PostForm["endpoint"]...)
	p.Endpoints = append(p.Endpoints, strings.Split(r.PostFormValue("otherEndpoints"), ",")...)
	p.Normalize()
	return p
}

func (a *Admin) statusPagesList(w http.ResponseWriter, r *http.Request) {
	a.renderStatusPages(w, r, map[string]any{})
}

func (a *Admin) renderStatusPages(w http.ResponseWriter, r *http.Request, data map[string]any) {
	endpoints := a.knownEndpoints(r)
	base := badge.PublicBaseURL(a.deps.Config(), r)
	pages := a.deps.Settings.Settings().StatusPages
	rows := make([]statusPageRow, 0, len(pages))
	for _, p := range pages {
		rows = append(rows, statusPageRow{StatusPage: p, URL: base + "/status/" + p.Slug})
	}
	data["StatusPages"] = rows
	p, ok := data["StatusPageForm"].(config.StatusPage)
	if !ok {
		p, _ = a.deps.Settings.Settings().StatusPage(r.URL.Query().Get("statusPage"))
	}
	data["StatusPageForm"] = newStatusPageForm(p, endpoints)
	a.render(w, r, "statuspages.html", data)
}

// statusPagePassword returns the password hash to store: a new password, none when it is removed, or else the
// current one.
func statusPagePassword(r *http.Request, current string) (string, error) {
	if r.PostFormValue("removePassword") != "" {
		return "", nil
	}
	pass := r.PostFormValue("password")
	if pass == "" {
		return current, nil
	}
	if len(pass) < minPassword {
		return current, errors.New("the password of the status page must have at least 10 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	return string(hash), err
}

func (a *Admin) statusPageSave(w http.ResponseWriter, r *http.Request) {
	p := statusPageFromForm(r)
	creating := p.ID == ""
	if creating {
		p.ID = settings.NewID()
	}
	current, _ := a.deps.Settings.Settings().StatusPage(p.ID)
	hash, err := statusPagePassword(r, current.PasswordBcrypt)
	p.PasswordBcrypt = hash
	if err == nil {
		err = a.deps.Settings.Update(func(s *settingsT) error {
			for i := range s.StatusPages {
				if s.StatusPages[i].ID == p.ID {
					s.StatusPages[i] = p
					return nil
				}
			}
			s.StatusPages = append(s.StatusPages, p)
			return nil
		})
	}
	if err != nil {
		if creating {
			p.ID = ""
		}
		p.PasswordBcrypt = current.PasswordBcrypt
		a.renderStatusPages(w, r, map[string]any{"StatusPageForm": p, "StatusPageError": err.Error()})
		return
	}
	redirect(w, r, "/status-pages", "Status page "+p.Slug+" saved.")
}

func (a *Admin) statusPageDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var slug string
	err := a.deps.Settings.Update(func(s *settingsT) error {
		kept := s.StatusPages[:0]
		for _, p := range s.StatusPages {
			if p.ID == id {
				slug = p.Slug
				continue
			}
			kept = append(kept, p)
		}
		s.StatusPages = kept
		return nil
	})
	msg := "Status page " + slug + " deleted."
	if err != nil {
		msg = "Deleting failed: " + err.Error()
	}
	redirect(w, r, "/status-pages", msg)
}
