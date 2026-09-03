package dashboard

import (
	"bytes"
	"testing"

	"github.com/dimension/ai-ci-agent/internal/githubauth"
)

// TestTemplatesParse catches a broken template at test time rather than
// on a package's first real page load -- templates.templates is parsed
// once via template.Must at package init, so a syntax error there would
// otherwise only surface the first time any test in this package runs
// at all, with a less useful panic message.
func TestTemplatesParse(t *testing.T) {
	if templates.Lookup("login.html") == nil {
		t.Fatal("login.html did not parse into the template set")
	}
	if templates.Lookup("index.html") == nil {
		t.Fatal("index.html did not parse into the template set")
	}
}

func TestLoginTemplate_RendersWithAndWithoutError(t *testing.T) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "login.html", loginPageData{Org: "dashlytix"}); err != nil {
		t.Fatalf("render without error: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("dashlytix")) {
		t.Error("expected the org name to appear in the rendered page")
	}

	buf.Reset()
	if err := templates.ExecuteTemplate(&buf, "login.html", loginPageData{Org: "dashlytix", Error: "not an admin"}); err != nil {
		t.Fatalf("render with error: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("not an admin")) {
		t.Error("expected the error message to appear in the rendered page")
	}
}

func TestIndexTemplate_RendersInstallationsAndEmptyState(t *testing.T) {
	var buf bytes.Buffer
	data := indexPageData{
		Login:      "octocat",
		InstallURL: "https://github.com/apps/dashlytix-pr-review-agent/installations/new",
		Installations: []installationView{
			{
				Installation: githubauth.Installation{
					Account:             githubauth.InstallationAccount{Login: "dashlytix", Type: "Organization"},
					RepositorySelection: "selected",
					HTMLURL:             "https://github.com/organizations/dashlytix/settings/installations/1",
				},
				Repos: []githubauth.Repository{{FullName: "dashlytix/dash-ai-agent"}},
			},
		},
	}
	if err := templates.ExecuteTemplate(&buf, "index.html", data); err != nil {
		t.Fatalf("render with installations: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("dashlytix/dash-ai-agent")) {
		t.Error("expected the covered repo to appear in the rendered page")
	}

	buf.Reset()
	if err := templates.ExecuteTemplate(&buf, "index.html", indexPageData{Login: "octocat", InstallURL: "https://x"}); err != nil {
		t.Fatalf("render with zero installations: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("not installed anywhere")) {
		t.Error("expected the empty-state message when there are no installations")
	}
}
