package model

import (
	"net/url"
	"strings"

	"github.com/artipop/xxvi/internal/msg"
)

// Proxy is one named network path: where an agent's traffic goes and the trust
// material that goes with it. Agents name an entry instead of carrying their
// own, so one configuration serves several of them and is changed in one place.
type Proxy struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`     // http(s)/socks5 → HTTP(S)_PROXY, ALL_PROXY
	NoProxy string `json:"noProxy,omitempty"` // comma-separated hosts → NO_PROXY
	CACert  string `json:"caCert,omitempty"`  // path to the PEM bundle of a TLS-inspecting proxy: the variables below take a file, not its text

	// Username and Password are kept apart from the URL so they are typed raw;
	// percent-encoding is applied when the URL is composed.
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// IsZero reports whether nothing is configured.
func (p Proxy) IsZero() bool {
	return p.URL == "" && p.NoProxy == "" && p.CACert == ""
}

// Address is the proxy URL with the credentials applied. Credentials given as
// fields win over any carried by the URL itself.
func (p Proxy) Address() (string, error) {
	raw := strings.TrimSpace(p.URL)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		// A bare host:port is read by the CLIs as nothing at all, which looks
		// like "the proxy setting does nothing".
		return "", msg.Err("proxy.badURL", "url", raw)
	}
	if user := strings.TrimSpace(p.Username); user != "" {
		if p.Password == "" {
			u.User = url.User(user)
		} else {
			u.User = url.UserPassword(user, p.Password)
		}
	}
	return u.String(), nil
}

// Validate normalizes the entry and checks it. kind is the agent kind it will
// run under, or "" to skip the kind-specific checks.
func (p Proxy) Validate(kind string) (Proxy, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.URL = strings.TrimSpace(p.URL)
	p.NoProxy = strings.TrimSpace(p.NoProxy)
	p.CACert = strings.TrimSpace(p.CACert)
	p.Username = strings.TrimSpace(p.Username)
	if p.Name == "" {
		return p, msg.Err("proxy.noName")
	}
	if p.URL == "" && (p.Username != "" || p.Password != "") {
		return p, msg.Err("proxy.credsWithoutURL")
	}
	if _, err := p.Address(); err != nil {
		return p, err
	}
	// Claude Code documents no SOCKS support, so a socks:// value would be
	// accepted here and then quietly ignored.
	if kind == KindClaude && strings.HasPrefix(strings.ToLower(p.URL), "socks") {
		return p, msg.Err("proxy.claudeSocks")
	}
	if kind == "" && p.IsZero() {
		return p, msg.Err("proxy.empty", "proxy", p.Name)
	}
	return p, nil
}

// Env is the standard proxy variables this entry expands to. Both cases are
// set: Node-based CLIs read the upper-case ones, Rust/Go/curl ones either.
// NO_PROXY is set together with the proxy so an inherited one cannot leak into
// an agent that goes through its own.
func (p Proxy) Env() []string {
	var env []string
	add := func(names []string, v string) {
		for _, k := range names {
			env = append(env, k+"="+v)
		}
	}
	addr, err := p.Address()
	if err != nil {
		addr = strings.TrimSpace(p.URL)
	}
	noProxy := []string{"NO_PROXY", "no_proxy"}
	if addr != "" {
		add([]string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}, addr)
		add(noProxy, p.NoProxy)
	} else if p.NoProxy != "" {
		add(noProxy, p.NoProxy)
	}
	if p.CACert != "" {
		// Node (claude), Rust/Python (codex and friends), curl.
		add([]string{"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE"}, p.CACert)
	}
	return env
}

// Redact hides the proxy password in text, raw or percent-encoded, so a CLI
// error echoing the proxy URL cannot carry it into a comment or the log.
func (p Proxy) Redact(text string) string {
	if p.Password == "" {
		return text
	}
	forms := []string{p.Password, url.QueryEscape(p.Password)}
	// The form that travels in the URL: userinfo escaping is its own set.
	if enc := url.UserPassword("u", p.Password).String(); strings.Contains(enc, ":") {
		forms = append(forms, enc[strings.Index(enc, ":")+1:])
	}
	for _, secret := range forms {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "***")
		}
	}
	return text
}
