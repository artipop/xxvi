package model

import (
	"strings"
	"testing"
)

func TestProxyEnvAndRedact(t *testing.T) {
	p := Proxy{Name: "x", URL: "http://h:1", Username: "u", Password: "p@:/w", NoProxy: "localhost"}
	addr, err := p.Address()
	if err != nil || strings.Contains(addr, "p@:/w") || !strings.HasPrefix(addr, "http://u:") {
		t.Fatalf("пароль не закодирован: %q, %v", addr, err)
	}
	if got := p.Redact("dial " + addr + " failed"); strings.Contains(got, "%") || !strings.Contains(got, "***") {
		t.Fatalf("пароль остался в тексте: %q", got)
	}
	env := strings.Join(p.Env(), "\n")
	for _, want := range []string{"HTTPS_PROXY=" + addr, "no_proxy=localhost"} {
		if !strings.Contains(env, want) {
			t.Fatalf("нет %q в %s", want, env)
		}
	}
	if _, err := (Proxy{Name: "x", URL: "host:3128"}).Validate(""); err == nil {
		t.Fatal("адрес без схемы принят")
	}
}
