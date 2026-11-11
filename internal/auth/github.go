package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

const githubAPIURL = "https://api.github.com"

type GitHubConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	AllowedUsers []string
	AuthURL      string
	TokenURL     string
	APIURL       string
}

type GitHubUser struct {
	ID    string
	Login string
}

type GitHub struct {
	oauth   oauth2.Config
	apiURL  string
	allowed map[string]bool
}

func NewGitHub(c GitHubConfig) (*GitHub, error) {
	if c.ClientID == "" || c.ClientSecret == "" || c.RedirectURL == "" {
		return nil, errors.New("github client id, client secret and redirect url are required")
	}

	endpoint := endpoints.GitHub
	if c.AuthURL != "" {
		endpoint.AuthURL = c.AuthURL
	}
	if c.TokenURL != "" {
		endpoint.TokenURL = c.TokenURL
	}

	apiURL := githubAPIURL
	if c.APIURL != "" {
		apiURL = strings.TrimSuffix(c.APIURL, "/")
	}

	allowed := map[string]bool{}
	for _, login := range c.AllowedUsers {
		if login = strings.TrimSpace(login); login != "" {
			allowed[strings.ToLower(login)] = true
		}
	}

	return &GitHub{
		oauth: oauth2.Config{
			ClientID:     c.ClientID,
			ClientSecret: c.ClientSecret,
			RedirectURL:  c.RedirectURL,
			Endpoint:     endpoint,
		},
		apiURL:  apiURL,
		allowed: allowed,
	}, nil
}

func (g *GitHub) SecureCookies() bool {
	return strings.HasPrefix(g.oauth.RedirectURL, "https://")
}

func (g *GitHub) Allowed(login string) bool {
	return len(g.allowed) == 0 || g.allowed[strings.ToLower(login)]
}

func NewVerifier() string {
	return oauth2.GenerateVerifier()
}

func (g *GitHub) AuthCodeURL(state, verifier string) string {
	return g.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
}

func (g *GitHub) User(ctx context.Context, code, verifier string) (GitHubUser, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	token, err := g.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return GitHubUser{}, fmt.Errorf("exchange github code: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.apiURL+"/user", nil)
	if err != nil {
		return GitHubUser{}, fmt.Errorf("github user: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := g.oauth.Client(ctx, token).Do(req)
	if err != nil {
		return GitHubUser{}, fmt.Errorf("github user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return GitHubUser{}, fmt.Errorf("github user: status %d", resp.StatusCode)
	}

	var body struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return GitHubUser{}, fmt.Errorf("github user: decode: %w", err)
	}

	if body.ID == 0 || body.Login == "" {
		return GitHubUser{}, errors.New("github user: missing id or login")
	}

	return GitHubUser{ID: strconv.FormatInt(body.ID, 10), Login: body.Login}, nil
}
