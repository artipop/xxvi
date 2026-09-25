package kaiten

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeKaiten answers the two questions the feed asks: who the token is, and
// the cards by one filter. Card 2 is both mine and one I am a member of.
func fakeKaiten(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/latest/users/current":
			_, _ = w.Write([]byte(`{"id": 7}`))
		case r.URL.Path == "/api/latest/cards" && r.URL.Query().Get("responsible_id") == "7":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "title": "Моя", "updated": "u1", "board": map[string]any{"space_id": 5}},
				{"id": 2, "title": "Общая", "updated": "u2"},
			})
		case r.URL.Path == "/api/latest/cards" && r.URL.Query().Get("member_ids") == "7":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 2, "title": "Общая", "updated": "u2"},
				{"id": 3, "title": "Помогаю", "updated": "u3"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// "Assigned to me" is two fields in Kaiten and one phrase to a person: both are
// asked, and a card that is both is one card.
func TestMyCardsAreWhereIAmResponsibleOrAMember(t *testing.T) {
	srv := fakeKaiten(t)
	cfg, err := Config{Site: srv.URL, Token: "t0k"}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	cards, err := ListMyCards(context.Background(), srv.Client(), cfg, listInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 3 {
		t.Fatalf("cards: %+v", cards)
	}
	if cards[0].URL != srv.URL+"/space/5/card/1" || cards[1].URL != srv.URL+"/ticket/2" {
		t.Fatalf("links: %q, %q", cards[0].URL, cards[1].URL)
	}

	only, err := ListMyCards(context.Background(), srv.Client(), cfg, listInput{ResponsibleOnly: true})
	if err != nil || len(only) != 2 {
		t.Fatalf("responsible only: %+v, %v", only, err)
	}
}

// A refused token is said with its status, because the status is the diagnosis.
func TestARefusedTokenSaysSo(t *testing.T) {
	srv := fakeKaiten(t)
	cfg, _ := Config{Site: srv.URL, Token: "wrong"}.Validate()
	_, err := ListMyCards(context.Background(), srv.Client(), cfg, listInput{})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err: %v", err)
	}
}

// A person types the site without the scheme more often than with it.
func TestASiteWithoutSchemeIsFixed(t *testing.T) {
	cfg, err := Config{Site: "company.kaiten.ru/", Token: "t"}.Validate()
	if err != nil || cfg.Site != "https://company.kaiten.ru" {
		t.Fatalf("site: %q, %v", cfg.Site, err)
	}
}
