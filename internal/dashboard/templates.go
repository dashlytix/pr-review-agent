package dashboard

import (
	"embed"
	"html/template"

	"github.com/dimension/ai-ci-agent/internal/githubauth"
)

//go:embed templates/*.html
var templateFS embed.FS

// templates is parsed once at package init -- a broken template is a
// programmer error caught at process startup (or by
// TestTemplatesParse), not something that should fail differently
// depending on which page a visitor happens to load first.
var templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// loginPageData is login.html's template data.
type loginPageData struct {
	Org   string
	Error string
}

// installationView pairs one githubauth.Installation with the repos it
// covers -- ListInstallationRepositories returns those separately, and
// index.html wants them alongside each row rather than as a second,
// separately-iterated list.
type installationView struct {
	githubauth.Installation
	Repos []githubauth.Repository
}

// indexPageData is index.html's template data.
type indexPageData struct {
	Login         string
	InstallURL    string
	Installations []installationView
}
