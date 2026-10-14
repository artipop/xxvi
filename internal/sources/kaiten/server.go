// Package kaiten is an MCP server over the Kaiten API, exposing the one thing a
// source needs: the cards assigned to whoever the token belongs to.
//
// It is this app's own binary re-invoked (`gandvik mcp kaiten`) rather than a
// server to install: a source somebody adds from the dialog must not need a
// runtime, a checkout on disk and a path typed into a form. The same server
// lives in XCIII; it is copied rather than shared because the two applications
// are released separately.
//
// Everything it may touch — which site, whose token — arrives in the
// environment, so the model chooses nothing about where it looks.
package kaiten

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerName is how the tools appear to an agent, and the name the manifest
// starts this server by: mcp__kaiten__list_my_cards.
const ServerName = "kaiten"

const version = "0.1.0"

// Environment this server is configured through.
const (
	EnvSite  = "KAITEN_SITE"  // https://company.kaiten.ru
	EnvToken = "KAITEN_TOKEN" // a personal API token from the Kaiten profile
)

const instructions = `Reading Kaiten: the cards assigned to the owner of the token.
The site and the token are set from outside and the tool does not choose them.`

// Config is what the process is given.
type Config struct {
	Site  string
	Token string
}

// Validate normalizes the site and refuses what cannot answer.
func (c Config) Validate() (Config, error) {
	c.Site = strings.TrimSuffix(strings.TrimSpace(c.Site), "/")
	c.Token = strings.TrimSpace(c.Token)
	if c.Site == "" {
		return c, fmt.Errorf("the Kaiten address is not set (%s)", EnvSite)
	}
	if !strings.HasPrefix(c.Site, "http://") && !strings.HasPrefix(c.Site, "https://") {
		// A person types "company.kaiten.ru" more often than the scheme, and
		// refusing that would be a form failing over what it could have fixed.
		c.Site = "https://" + c.Site
	}
	if c.Token == "" {
		return c, fmt.Errorf("the Kaiten token is not set (%s)", EnvToken)
	}
	return c, nil
}

func (c Config) api(path string) string { return c.Site + "/api/latest" + path }

// listInput is what the feed is asked for. Every field is optional: with none
// of them this is "everything assigned to me anywhere".
type listInput struct {
	BoardID         int  `json:"boardId,omitempty" jsonschema:"only the cards of this Kaiten board"`
	SpaceID         int  `json:"spaceId,omitempty" jsonschema:"only the cards of this space"`
	ResponsibleOnly bool `json:"responsibleOnly,omitempty" jsonschema:"only where I am responsible, not counting membership"`
	IncludeArchived bool `json:"includeArchived,omitempty" jsonschema:"including archived and finished ones"`
	Limit           int  `json:"limit,omitempty" jsonschema:"how many cards at most (100 by default)"`
}

// card is what the feed hands over. It is Kaiten's own row plus the address a
// person would open, which the API does not carry.
type card struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Updated     string `json:"updated,omitempty"`
	URL         string `json:"url"`
	Column      string `json:"column,omitempty"`
	Board       string `json:"board,omitempty"`
}

// NewServer builds the server. client is injectable so a test can answer for
// Kaiten without one.
func NewServer(cfg Config, client *http.Client) *mcp.Server {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	srv := mcp.NewServer(
		&mcp.Implementation{Name: ServerName, Title: "Kaiten", Version: version},
		&mcp.ServerOptions{Instructions: instructions},
	)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_my_cards",
		Description: "The cards assigned to the owner of the token: where they are responsible and, unless told otherwise, where they are a member.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, any, error) {
		cards, err := ListMyCards(ctx, client, cfg, in)
		if err != nil {
			return errorResult("%v", err), nil, nil
		}
		out, err := json.MarshalIndent(map[string]any{"cards": cards}, "", "  ")
		if err != nil {
			return errorResult("%v", err), nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(out)}}}, nil, nil
	})
	return srv
}

// ServeStdio runs the server on stdio, which is how a source starts it.
func ServeStdio(ctx context.Context, cfg Config) error {
	valid, err := cfg.Validate()
	if err != nil {
		return err
	}
	return NewServer(valid, nil).Run(ctx, &mcp.StdioTransport{})
}

// ListMyCards asks Kaiten twice and merges by id.
//
// Kaiten has no single "assigned to me": an owner field and a member are
// different fields, and a person means both by the phrase. Two questions and a
// merge is the honest way to ask it — the alternative is a filter language of
// theirs that would have to be guessed at.
func ListMyCards(ctx context.Context, client *http.Client, cfg Config, in listInput) ([]card, error) {
	me, err := currentUserID(ctx, client, cfg)
	if err != nil {
		return nil, err
	}

	base := url.Values{}
	if in.BoardID != 0 {
		base.Set("board_id", strconv.Itoa(in.BoardID))
	}
	if in.SpaceID != 0 {
		base.Set("space_id", strconv.Itoa(in.SpaceID))
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	base.Set("limit", strconv.Itoa(limit))
	if !in.IncludeArchived {
		// condition=1 is Kaiten's "live": a card that is done is not something
		// to hand somebody again tomorrow.
		base.Set("condition", "1")
		base.Set("archived", "false")
	}

	filters := []string{"responsible_id=" + strconv.Itoa(me)}
	if !in.ResponsibleOnly {
		filters = append(filters, "member_ids="+strconv.Itoa(me))
	}

	seen := map[int]bool{}
	out := make([]card, 0, 16)
	for _, filter := range filters {
		var rows []struct {
			ID          int    `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Updated     string `json:"updated"`
			Board       struct {
				ID      int    `json:"id"`
				Title   string `json:"title"`
				SpaceID int    `json:"space_id"`
			} `json:"board"`
			Column struct {
				Title string `json:"title"`
			} `json:"column"`
		}
		if err := get(ctx, client, cfg, "/cards?"+base.Encode()+"&"+filter, &rows); err != nil {
			return nil, err
		}
		for _, row := range rows {
			if seen[row.ID] {
				// The same card can be both, and a card is one card.
				continue
			}
			seen[row.ID] = true
			out = append(out, card{
				ID: row.ID, Title: row.Title, Description: row.Description,
				Updated: row.Updated, Column: row.Column.Title, Board: row.Board.Title,
				URL: cardURL(cfg, row.ID, row.Board.SpaceID),
			})
		}
	}
	return out, nil
}

// cardURL is the address a person opens. The API does not carry one, and a card
// in an inbox with no way back to it is a card you have to search for.
func cardURL(cfg Config, cardID, spaceID int) string {
	if spaceID > 0 {
		return fmt.Sprintf("%s/space/%d/card/%d", cfg.Site, spaceID, cardID)
	}
	return fmt.Sprintf("%s/ticket/%d", cfg.Site, cardID)
}

func currentUserID(ctx context.Context, client *http.Client, cfg Config) (int, error) {
	var me struct {
		ID int `json:"id"`
	}
	if err := get(ctx, client, cfg, "/users/current", &me); err != nil {
		return 0, err
	}
	if me.ID == 0 {
		return 0, fmt.Errorf("Kaiten did not say whose token this is")
	}
	return me.ID, nil
}

func get(ctx context.Context, client *http.Client, cfg Config, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.api(path), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		// The status is the whole of the diagnosis here — 401 is a token, 404 is
		// a site — so it goes back in the words the dialog will show.
		return fmt.Errorf("Kaiten answered %d to %s: %s", resp.StatusCode, path, strings.TrimSpace(truncate(string(body))))
	}
	return json.Unmarshal(body, out)
}

func truncate(s string) string {
	const limit = 300
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

func errorResult(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}
