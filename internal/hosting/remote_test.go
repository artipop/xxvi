package hosting

import "testing"

func TestRemoteForms(t *testing.T) {
	cases := []struct {
		raw  string
		want Remote
	}{
		{"git@gitlab.com:group/repo.git", Remote{Web: "https://gitlab.com", Path: "group/repo"}},
		{"git@gitlab.company.ru:a/b/c/repo.git", Remote{Web: "https://gitlab.company.ru", Path: "a/b/c/repo"}},
		{"ssh://git@gitlab.company.ru:2222/group/repo.git", Remote{Web: "https://gitlab.company.ru", Path: "group/repo"}},
		{"https://gitlab.com/group/sub/repo.git", Remote{Web: "https://gitlab.com", Path: "group/sub/repo"}},
		{"https://oauth2:secret@gitlab.com/group/repo", Remote{Web: "https://gitlab.com", Path: "group/repo"}},
		{"http://127.0.0.1:8080/group/repo.git/", Remote{Web: "http://127.0.0.1:8080", Path: "group/repo"}},
	}
	for _, c := range cases {
		got, ok := ParseRemote(c.raw)
		if !ok || got != c.want {
			t.Errorf("%s: получено %+v (%v), ожидалось %+v", c.raw, got, ok, c.want)
		}
	}
	for _, raw := range []string{"", "/home/me/repo", `C:\repo`, "https://gitlab.com/repo", "file:///tmp/repo.git"} {
		if r, ok := ParseRemote(raw); ok {
			t.Errorf("%q — не адрес хостинга, а разобран как %+v", raw, r)
		}
	}
}

// A token must not leak into the address a server is known by: the remote
// may carry one, and that address is shown and used as a keychain key.
func TestRemoteDropsCredentials(t *testing.T) {
	r, _ := ParseRemote("https://oauth2:secret@gitlab.com/group/repo.git")
	if r.Web != "https://gitlab.com" {
		t.Fatalf("учётка попала в адрес сервера: %s", r.Web)
	}
}

func TestProviderGuess(t *testing.T) {
	if p := GuessProvider(Remote{Web: "https://gitlab.company.ru", Path: "a/b"}); p != "gitlab" {
		t.Fatalf("gitlab.company.ru — GitLab, получено %q", p)
	}
	if p := GuessProvider(Remote{Web: "https://git.company.ru", Path: "a/b"}); p != "" {
		t.Fatalf("по имени git.company.ru провайдера не угадать, получено %q", p)
	}
}
