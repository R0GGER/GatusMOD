package settings

import (
	"strings"
	"testing"
	"time"

	"gatusmod/internal/config"
)

func TestUpdate_StatusPages(t *testing.T) {
	dir := t.TempDir()
	m, err := Open(dir, baseConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	page := config.StatusPage{ID: "p1", Slug: " Klant-A ", Domains: []string{"https://Status.Klant.nl/", ""}, Groups: []string{"core", " core ", "web"}, Title: "Klant A"}
	if err := m.Update(func(s *Settings) error { s.StatusPages = append(s.StatusPages, page); return nil }); err != nil {
		t.Fatal(err)
	}
	got := m.Get().StatusPageByHost("status.klant.nl:443")
	if got == nil || got.Slug != "klant-a" || len(got.Groups) != 2 || got.Title != "Klant A" {
		t.Fatalf("status page not applied: %+v", got)
	}
	if stored, _ := m.Settings().StatusPage("p1"); len(stored.Domains) != 1 || stored.Domains[0] != "status.klant.nl" {
		t.Errorf("the normalized page should be stored, got %+v", stored)
	}

	err = m.Update(func(s *Settings) error {
		s.StatusPages = append(s.StatusPages, config.StatusPage{ID: "p2", Slug: "other", Domains: []string{"status.klant.nl"}, Groups: []string{"x"}})
		return nil
	})
	if err == nil {
		t.Error("a domain used by two status pages should be rejected")
	}

	if err := m.Update(func(s *Settings) error { s.General = General{}; return nil }); err != nil {
		t.Fatal(err)
	}
	m2, err := Open(dir, baseConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if m2.Get().StatusPageBySlug("klant-a") == nil {
		t.Error("status pages should be saved and survive resetting the general settings")
	}
}

func TestBackup_StatusPages(t *testing.T) {
	cur := Settings{StatusPages: []config.StatusPage{{ID: "a", Slug: "klant-a", Groups: []string{"x"}}, {ID: "b", Slug: "klant-b", Groups: []string{"y"}}}}
	data, err := NewBackup(Settings{StatusPages: []config.StatusPage{{ID: "other", Slug: "klant-a", Groups: []string{"z"}}, {ID: "c", Slug: "klant-c", Groups: []string{"c"}}}}, false, time.Now()).YAML()
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseBackup(data)
	if err != nil {
		t.Fatal(err)
	}
	merged := cur.clone()
	merged.Restore(b, false)
	if len(merged.StatusPages) != 3 || merged.StatusPages[0].ID != "a" || merged.StatusPages[0].Groups[0] != "z" {
		t.Errorf("merge should update klant-a by slug and add klant-c, got %+v", merged.StatusPages)
	}
	replaced := cur.clone()
	replaced.Restore(b, true)
	if len(replaced.StatusPages) != 2 || replaced.StatusPages[1].Slug != "klant-c" {
		t.Errorf("replace should keep only the status pages of the backup, got %+v", replaced.StatusPages)
	}
}

func TestBackup_PrivateStatusPages(t *testing.T) {
	src := Settings{StatusPages: []config.StatusPage{
		{ID: "a", Slug: "known", Groups: []string{"x"}, PasswordBcrypt: "hash-a"},
		{ID: "b", Slug: "elsewhere", Groups: []string{"y"}, PasswordBcrypt: "hash-b"},
		{ID: "c", Slug: "open", Groups: []string{"z"}},
	}}
	with := NewBackup(src, true, time.Now())
	if with.StatusPages[0].PasswordBcrypt != "hash-a" || len(with.PrivateStatusPages) != 0 {
		t.Errorf("with tokens the password hashes are kept: %+v", with)
	}
	data, err := NewBackup(src, false, time.Now()).YAML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hash-") {
		t.Fatalf("without tokens the password hashes must be left out:\n%s", data)
	}
	b, err := ParseBackup(data)
	if err != nil {
		t.Fatal(err)
	}
	cur := Settings{StatusPages: []config.StatusPage{{ID: "a", Slug: "known", Groups: []string{"x"}, PasswordBcrypt: "current"}}}
	res := cur.Restore(b, true)
	if cur.StatusPages[0].PasswordBcrypt != "current" {
		t.Errorf("a page that exists keeps its password, got %q", cur.StatusPages[0].PasswordBcrypt)
	}
	if p := cur.StatusPages[1]; !p.Private() || p.PasswordBcrypt == "hash-b" || strings.Join(res.LockedStatusPages, ",") != "elsewhere" {
		t.Errorf("a new private page must stay private with an unknown password: %+v %v", p, res.LockedStatusPages)
	}
	if cur.StatusPages[2].Private() {
		t.Error("a public page stays public")
	}
}
