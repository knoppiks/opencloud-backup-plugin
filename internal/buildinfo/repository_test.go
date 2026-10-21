package buildinfo

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The repository's own front door (phase-11 §11.8, #137): the issue forms,
// the pull request template and the documents a stranger reads first. Every
// issue ends up in the release notes, sorted by its one kind/ label, and
// every pull request must close one (pr-issue.yml); the forms and the
// template start them off ready. GitHub accepts a broken form silently and
// just stops offering it, so these fail first.

// kinds are the kind/ labels an issue may carry (AGENTS.md).
var kinds = []string{"Feature", "Enhancement", "Bug", "Refactoring", "Epic", "Question"}

const privateReportingURL = "https://github.com/knoppiks/opencloud-backup-plugin/security/advisories/new"

type issueForm struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description"`
	Labels      []string      `yaml:"labels"`
	Body        []formElement `yaml:"body"`
}

type formElement struct {
	Type        string `yaml:"type"`
	ID          string `yaml:"id"`
	Validations struct {
		Required bool `yaml:"required"`
	} `yaml:"validations"`
}

type contactLink struct {
	URL string `yaml:"url"`
}

func readIssueForm(t *testing.T, name string) issueForm {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, ".github/ISSUE_TEMPLATE", name))
	if err != nil {
		t.Fatal(err)
	}
	var form issueForm
	if err := yaml.Unmarshal(data, &form); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return form
}

func issueFormFiles(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(repoRoot, ".github/ISSUE_TEMPLATE/*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var forms []string
	for _, p := range paths {
		if filepath.Base(p) != "config.yml" {
			forms = append(forms, filepath.Base(p))
		}
	}
	if len(forms) == 0 {
		t.Fatal("no issue forms")
	}
	return forms
}

func TestEveryIssueFormSetsExactlyOneKind(t *testing.T) {
	for _, name := range issueFormFiles(t) {
		form := readIssueForm(t, name)
		if form.Name == "" || form.Description == "" || len(form.Body) == 0 {
			t.Errorf("%s: needs a name, a description and a body", name)
		}
		var kindLabels []string
		for _, l := range form.Labels {
			if k, ok := strings.CutPrefix(l, "kind/"); ok {
				kindLabels = append(kindLabels, l)
				if !slices.Contains(kinds, k) {
					t.Errorf("%s: %q is not a kind (%v)", name, l, kinds)
				}
			}
		}
		if len(kindLabels) != 1 {
			t.Errorf("%s: sets kind/ labels %v, needs exactly one", name, kindLabels)
		}
	}
}

func TestBugFormAsksForBothVersions(t *testing.T) {
	form := readIssueForm(t, "bug.yml")
	for _, id := range []string{"plugin-version", "opencloud-version"} {
		i := slices.IndexFunc(form.Body, func(e formElement) bool { return e.ID == id })
		if i < 0 || !form.Body[i].Validations.Required {
			t.Errorf("bug.yml: no required field %q", id)
		}
	}
}

func TestIssuesStartFromAFormOrReportPrivately(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".github/ISSUE_TEMPLATE/config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		BlankIssuesEnabled *bool         `yaml:"blank_issues_enabled"`
		ContactLinks       []contactLink `yaml:"contact_links"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.BlankIssuesEnabled == nil || *cfg.BlankIssuesEnabled {
		t.Error("config.yml: blank issues are allowed; an issue without a form has no kind")
	}
	if !slices.ContainsFunc(cfg.ContactLinks, func(l contactLink) bool { return l.URL == privateReportingURL }) {
		t.Errorf("config.yml: no contact link to %s", privateReportingURL)
	}
}

func TestSecurityPolicyPointsAtPrivateReporting(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "SECURITY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), privateReportingURL) {
		t.Errorf("SECURITY.md does not link %s", privateReportingURL)
	}
}

// pr-issue.yml reads the closing references GitHub derives from the
// description; a template ending in the keyword leaves only the number to
// fill in.
func TestPullRequestTemplateEndsWithAClosingKeyword(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".github/pull_request_template.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if last := lines[len(lines)-1]; last != "Closes #" {
		t.Errorf("pull_request_template.md ends with %q, want \"Closes #\"", last)
	}
}

var (
	markdownLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	heading      = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*$`)
	notSlugChar  = regexp.MustCompile(`[^\p{L}\p{N} _-]`)
)

// headingAnchors are the anchors GitHub gives a Markdown file's headings
// (lower case, punctuation dropped, spaces to hyphens), outside code blocks.
func headingAnchors(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	anchors := map[string]bool{}
	inFence := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if m := heading.FindStringSubmatch(line); m != nil && !inFence {
			slug := notSlugChar.ReplaceAllString(strings.ToLower(m[1]), "")
			anchors[strings.ReplaceAll(slug, " ", "-")] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return anchors
}

// The policy and the guide point into the README and the plans rather than
// repeat them; a renamed heading or a moved file must not leave them
// pointing at nothing.
func TestFrontDoorDocumentsLinkToWhatExists(t *testing.T) {
	for _, doc := range []string{"SECURITY.md", "CONTRIBUTING.md", ".github/pull_request_template.md"} {
		data, err := os.ReadFile(filepath.Join(repoRoot, doc))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range markdownLink.FindAllStringSubmatch(string(data), -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			file, anchor, _ := strings.Cut(target, "#")
			path := filepath.Join(repoRoot, filepath.Dir(doc), file)
			if file == "" {
				path = filepath.Join(repoRoot, doc)
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s: link %q: %v", doc, target, err)
				continue
			}
			if anchor != "" && !headingAnchors(t, path)[anchor] {
				t.Errorf("%s: link %q: no such heading in %s", doc, target, file)
			}
		}
	}
}

// The service image builds from the repository root and needs only the Go
// module (Dockerfile); the web extension has its own image and context.
func TestServiceImageContextLeavesOutTheWebAndBuildOutput(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".dockerignore"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for _, want := range []string{"web/", "**/node_modules/", "**/dist/"} {
		if !slices.Contains(lines, want) {
			t.Errorf(".dockerignore does not exclude %s", want)
		}
	}
}
