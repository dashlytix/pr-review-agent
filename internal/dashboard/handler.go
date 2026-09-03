package dashboard

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dimension/ai-ci-agent/internal/githubauth"
)

// oauthStateTTL bounds how long the CSRF state cookie set by /login is
// honored before /auth/callback must reject it -- long enough for a
// real GitHub consent screen, short enough that a stale, unused state
// value doesn't linger.
const oauthStateTTL = 5 * time.Minute

// Handler holds everything a request needs -- the App credential for
// GitHub-App-authenticated calls (ListInstallations,
// ListInstallationRepositories), the session codec, and this
// deployment's own OAuth client identity and origin. It deliberately
// holds no request state between calls: every page load re-derives
// admin status and the installation list live from GitHub (see
// handleIndex), matching this package's doc comment.
type Handler struct {
	App          *githubauth.AppAuthenticator
	Codec        *sessionCodec
	Org          string
	AppSlug      string
	ClientID     string
	ClientSecret string
	// BaseURL is this server's own externally-reachable origin (no
	// trailing slash) -- builds the OAuth redirect_uri and decides
	// whether cookies get the Secure attribute.
	BaseURL string
	// HTTP is used for every outbound call this Handler makes as the
	// signed-in user (OAuth exchange, GET /user, org membership).
	// Defaults to http.DefaultClient if nil, matching
	// githubauth.AppAuthenticator's own HTTP field.
	HTTP *http.Client

	// APIBaseURL/OAuthBaseURL override GitHub's real hosts. Empty in
	// production; set only by tests to point at an httptest.Server.
	APIBaseURL   string
	OAuthBaseURL string
}

// Register wires every route this Handler serves onto mux, using Go
// 1.22's method+pattern ServeMux matching.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", h.handleIndex)
	mux.HandleFunc("GET /login", h.handleLogin)
	mux.HandleFunc("GET /auth/callback", h.handleCallback)
	mux.HandleFunc("GET /logout", h.handleLogout)
}

// handleIndex is the only page this dashboard has: signed out (or no
// longer an admin) shows the login page: signed in *and* currently a
// live-verified admin shows the installations list. There is no
// separate "protected route" to guard with middleware -- this is it.
func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.currentSession(r)
	if !ok {
		clearCookie(w, sessionCookieName)
		h.renderLogin(w, errorMessage(r.URL.Query().Get("error")))
		return
	}

	// Re-derived live on every request, never cached from login time --
	// see this package's doc comment for why.
	admin, err := isOrgAdmin(r.Context(), h.httpClient(), h.apiBase(), sess.UserToken, h.Org)
	if err != nil {
		// A GitHub outage is not "confirmed not admin" -- fail the
		// request, don't silently downgrade access.
		http.Error(w, "checking org membership: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !admin {
		clearCookie(w, sessionCookieName)
		h.renderLogin(w, fmt.Sprintf("You are not an active admin of the %s organization.", h.Org))
		return
	}

	installations, err := h.App.ListInstallations(r.Context())
	if err != nil {
		http.Error(w, "listing installations: "+err.Error(), http.StatusBadGateway)
		return
	}

	views := make([]installationView, 0, len(installations))
	for _, inst := range installations {
		repos, err := h.App.ListInstallationRepositories(r.Context(), inst.ID)
		if err != nil {
			http.Error(w, "listing repositories: "+err.Error(), http.StatusBadGateway)
			return
		}
		views = append(views, installationView{Installation: inst, Repos: repos})
	}

	data := indexPageData{
		Login:         sess.Login,
		InstallURL:    fmt.Sprintf("https://github.com/apps/%s/installations/new", h.AppSlug),
		Installations: views,
	}
	if err := templates.ExecuteTemplate(w, "index.html", data); err != nil {
		log.Printf("dashboard: render index.html: %v", err)
	}
}

// handleLogin sets a fresh CSRF state cookie and redirects to GitHub's
// OAuth authorize endpoint. scope=read:org is the only scope requested
// -- isOrgAdmin is the only thing this dashboard ever does with the
// resulting user token.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomState()
	if err != nil {
		http.Error(w, "generating login state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oauthStateCookieName, Value: state, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: h.secureCookies(),
		MaxAge: int(oauthStateTTL.Seconds()),
	})

	authorizeURL := fmt.Sprintf("%s/login/oauth/authorize?client_id=%s&redirect_uri=%s&scope=read:org&state=%s",
		h.oauthBase(), url.QueryEscape(h.ClientID), url.QueryEscape(h.redirectURI()), url.QueryEscape(state))
	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

// handleCallback verifies the CSRF state, exchanges the code, checks
// org-admin status once (so a non-admin never gets a session cookie at
// all -- handleIndex's own live check is what protects every later
// request), and seals a session cookie on success.
func (h *Handler) handleCallback(w http.ResponseWriter, r *http.Request) {
	stateCookie, cookieErr := r.Cookie(oauthStateCookieName)
	clearCookie(w, oauthStateCookieName)
	queryState := r.URL.Query().Get("state")
	if cookieErr != nil || queryState == "" || subtle.ConstantTimeCompare([]byte(stateCookie.Value), []byte(queryState)) != 1 {
		h.redirectWithError(w, r, "Login state did not match -- please try signing in again.")
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		h.redirectWithError(w, r, "GitHub did not return an authorization code.")
		return
	}

	userToken, err := exchangeCode(r.Context(), h.httpClient(), h.oauthBase(), h.ClientID, h.ClientSecret, code, h.redirectURI())
	if err != nil {
		log.Printf("dashboard: oauth exchange failed: %v", err)
		h.redirectWithError(w, r, "Sign-in failed. Please try again.")
		return
	}

	admin, err := isOrgAdmin(r.Context(), h.httpClient(), h.apiBase(), userToken, h.Org)
	if err != nil {
		log.Printf("dashboard: org membership check failed: %v", err)
		h.redirectWithError(w, r, "Sign-in failed. Please try again.")
		return
	}
	if !admin {
		h.redirectWithError(w, r, fmt.Sprintf("You are not an active admin of the %s organization.", h.Org))
		return
	}

	login, err := githubLogin(r.Context(), h.httpClient(), h.apiBase(), userToken)
	if err != nil {
		log.Printf("dashboard: fetch login failed: %v", err)
		h.redirectWithError(w, r, "Sign-in failed. Please try again.")
		return
	}

	sealed, err := h.Codec.seal(session{Login: login, UserToken: userToken, ExpiresAt: time.Now().Add(sessionTTL)})
	if err != nil {
		log.Printf("dashboard: seal session failed: %v", err)
		h.redirectWithError(w, r, "Sign-in failed. Please try again.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: sealed, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: h.secureCookies(),
		MaxAge: int(sessionTTL.Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearCookie(w, sessionCookieName)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (h *Handler) renderLogin(w http.ResponseWriter, errMsg string) {
	if err := templates.ExecuteTemplate(w, "login.html", loginPageData{Org: h.Org, Error: errMsg}); err != nil {
		log.Printf("dashboard: render login.html: %v", err)
	}
}

func (h *Handler) redirectWithError(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/?error="+url.QueryEscape(msg), http.StatusFound)
}

// currentSession reads and opens the session cookie, reporting ok=false
// for a missing, malformed, tampered, or expired cookie alike -- the
// caller (handleIndex) doesn't need to distinguish those cases, it just
// falls back to the login page either way.
func (h *Handler) currentSession(r *http.Request) (session, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return session{}, false
	}
	sess, err := h.Codec.open(cookie.Value, time.Now())
	if err != nil {
		return session{}, false
	}
	return sess, true
}

func (h *Handler) httpClient() *http.Client {
	if h.HTTP != nil {
		return h.HTTP
	}
	return http.DefaultClient
}

func (h *Handler) apiBase() string {
	if h.APIBaseURL != "" {
		return h.APIBaseURL
	}
	return defaultGitHubAPIURL
}

func (h *Handler) oauthBase() string {
	if h.OAuthBaseURL != "" {
		return h.OAuthBaseURL
	}
	return defaultGitHubOAuthURL
}

func (h *Handler) redirectURI() string {
	return h.BaseURL + "/auth/callback"
}

func (h *Handler) secureCookies() bool {
	return strings.HasPrefix(h.BaseURL, "https://")
}

// clearCookie expires name immediately -- used both to log out and to
// scrub a cookie that failed validation, so a bad value never lingers
// in the browser past the request that noticed it.
func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

// errorMessage maps the small, fixed set of query-param error codes
// this package's own redirects produce back to the literal message
// text -- redirectWithError already puts human-readable text straight
// in the query param, so this is just a passthrough today, kept as its
// own function so handleIndex has one place to extend if that ever
// needs to change (e.g. mapping opaque codes instead of raw text).
func errorMessage(raw string) string {
	return raw
}

// randomState returns a URL-safe random value for the OAuth CSRF state
// parameter.
func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
