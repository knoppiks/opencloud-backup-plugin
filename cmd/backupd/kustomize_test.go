package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The Kustomize base (deploy/kustomize, #159). These tests hold what it
// promises and what the release (#160) relies on; that it renders and
// validates is `make k8s-validate`, that its NetworkPolicy holds is
// `make k8s-netpol-test`.

const (
	kustomizeBase    = "../../deploy/kustomize/base/"
	kustomizeExample = "../../deploy/kustomize/overlays/example/"
)

// unreleased is what the repository's base names instead of a release; a
// release archive replaces it (#160).
const unreleased = "unreleased"

type kustomization struct {
	Resources []string `yaml:"resources"`
	Labels    []struct {
		Pairs            map[string]string `yaml:"pairs"`
		IncludeSelectors bool              `yaml:"includeSelectors"`
	} `yaml:"labels"`
	Images []struct {
		Name   string `yaml:"name"`
		NewTag string `yaml:"newTag"`
		Digest string `yaml:"digest"`
	} `yaml:"images"`
}

func readKustomization(t *testing.T, dir string) kustomization {
	t.Helper()
	body, err := os.ReadFile(dir + "kustomization.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var k kustomization
	if err := yaml.Unmarshal(body, &k); err != nil {
		t.Fatalf("%skustomization.yaml: %v", dir, err)
	}
	return k
}

// k8sObject is the part of a manifest these tests read.
type k8sObject struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name   string            `yaml:"name"`
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec yaml.Node `yaml:"spec"`
}

func readObjects(t *testing.T, path string) []k8sObject {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var objects []k8sObject
	dec := yaml.NewDecoder(bytes.NewReader(body))
	for {
		var o k8sObject
		if err := dec.Decode(&o); err != nil {
			if err.Error() == "EOF" {
				return objects
			}
			t.Fatalf("%s: %v", path, err)
		}
		objects = append(objects, o)
	}
}

// The base names no release: its image and its version label say
// `unreleased`, in the form the release stamps. Every image of this project
// in it goes through `images:`, so stamping one place covers them all.
func TestKustomizeBaseNamesNoRelease(t *testing.T) {
	k := readKustomization(t, kustomizeBase)

	if len(k.Images) != 1 || k.Images[0].Name != "ghcr.io/knoppiks/opencloud-backupd" ||
		k.Images[0].NewTag != unreleased || k.Images[0].Digest != "" {
		t.Errorf("images: want only ghcr.io/knoppiks/opencloud-backupd at newTag %q, got %+v", unreleased, k.Images)
	}

	var version bool
	for _, l := range k.Labels {
		if v, ok := l.Pairs["app.kubernetes.io/version"]; ok {
			version = true
			if v != unreleased {
				t.Errorf("app.kubernetes.io/version is %q, want %q", v, unreleased)
			}
			if l.IncludeSelectors {
				t.Error("the version label must stay off selectors: an existing Deployment's cannot change")
			}
		}
	}
	if !version {
		t.Error("the base sets no app.kubernetes.io/version label")
	}

	image := regexp.MustCompile(`(?m)^\s+image: (\S+)$`)
	for _, f := range k.Resources {
		body, err := os.ReadFile(kustomizeBase + f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range image.FindAllStringSubmatch(string(body), -1) {
			if m[1] != k.Images[0].Name {
				t.Errorf("%s names image %s; only the bare name, which `images:` sets", f, m[1])
			}
		}
	}
}

// Every manifest in the base is one of its resources: a file left out is a
// resource nobody deploys, and nothing else would notice.
func TestKustomizeBaseListsEveryManifest(t *testing.T) {
	k := readKustomization(t, kustomizeBase)
	files, err := filepath.Glob(kustomizeBase + "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := filepath.Base(f)
		if name != "kustomization.yaml" && !slices.Contains(k.Resources, name) {
			t.Errorf("%s is not in the base's resources", name)
		}
	}
	// A placeholder Secret in the base would replace the operator's real
	// keys on the next `kubectl apply -k`.
	for _, f := range k.Resources {
		for _, o := range readObjects(t, kustomizeBase+f) {
			if o.Kind == "Secret" {
				t.Errorf("%s: the base must hold no Secret (%s)", f, o.Metadata.Name)
			}
		}
	}
}

// The pods never reach the Kubernetes API, so none carries a token.
func TestKustomizeBaseMountsNoServiceAccountToken(t *testing.T) {
	for _, f := range []string{"serviceaccount.yaml", "deployment-backupd.yaml", "operator-jobs.yaml"} {
		body, err := os.ReadFile(kustomizeBase + f)
		if err != nil {
			t.Fatal(err)
		}
		pods := len(readObjects(t, kustomizeBase+f))
		if got := strings.Count(string(body), "automountServiceAccountToken: false"); got != pods {
			t.Errorf("%s: automountServiceAccountToken: false %d times, want once per object (%d)", f, got, pods)
		}
	}
}

// The operator CronJobs run commands the image has, and never run by
// themselves.
func TestKustomizeOperatorJobs(t *testing.T) {
	path := kustomizeBase + "operator-jobs.yaml"
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	objects := readObjects(t, path)
	if len(objects) != 4 {
		t.Fatalf("%d objects, want the four operator commands", len(objects))
	}
	for _, o := range objects {
		if o.Kind != "CronJob" {
			t.Errorf("%s is a %s; a Job's template is immutable, so the next apply after a change fails", o.Metadata.Name, o.Kind)
		}
		var spec struct {
			Suspend bool `yaml:"suspend"`
		}
		if err := o.Spec.Decode(&spec); err != nil {
			t.Fatal(err)
		}
		if !spec.Suspend {
			t.Errorf("%s is not suspended: it would run on its schedule", o.Metadata.Name)
		}
		if o.Metadata.Labels["app.kubernetes.io/component"] == "backupd" {
			t.Errorf("%s carries the service's labels: the Service would route to it", o.Metadata.Name)
		}
	}

	args := regexp.MustCompile(`(?m)^\s+args: \["([a-z-]+)"`).FindAllStringSubmatch(string(body), -1)
	if len(args) != 3 {
		t.Fatalf("%d backupd commands, want provision-state-space, rotate-srw, rotate-tw", len(args))
	}
	for _, m := range args {
		if _, ok := lookupCommand(m[1]); !ok {
			t.Errorf("a CronJob runs `backupd %s`, which is not a command", m[1])
		}
	}

	dockerfile, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `command: ["/takeout"]`) && !strings.Contains(string(dockerfile), "COPY --from=build /out/takeout /takeout") {
		t.Error("the takeout CronJob runs /takeout, which the image does not ship")
	}

	// provision-state-space refuses while STATE_SPACE_ID is set, and the
	// ConfigMap's value is the placeholder until it has run.
	provision, _, found := bytes.Cut(body, []byte("\n---\n"))
	if !found {
		t.Fatal("operator-jobs.yaml is one document; want one per CronJob")
	}
	if !regexp.MustCompile(`- name: STATE_SPACE_ID\s+value: ""`).Match(provision) {
		t.Error("the provision-state-space CronJob must clear STATE_SPACE_ID")
	}
}

// The Kubernetes versions the manifests are validated against are the ones
// the docs name.
func TestKustomizeDocsNameTheValidatedRange(t *testing.T) {
	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	minor := func(name string) string {
		m := regexp.MustCompile(`(?m)^` + name + ` := (\d+\.\d+)\.\d+$`).FindSubmatch(makefile)
		if m == nil {
			t.Fatalf("the Makefile sets no %s", name)
		}
		return string(m[1])
	}
	want := "Kubernetes " + minor("K8S_OLDEST") + " to " + minor("K8S_NEWEST")
	readme, err := os.ReadFile("../../deploy/kustomize/README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), want) {
		t.Errorf("deploy/kustomize/README.md must say %q", want)
	}
}
