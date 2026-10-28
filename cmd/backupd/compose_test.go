package main

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/knoppiks/opencloud-backup-plugin/internal/config"
)

// The opencloud-compose add-on (deploy/compose, #156). These tests hold what
// it promises and what the release (#160) relies on; that it runs is shown
// against opencloud-compose itself (#158).

const composeDir = "../../deploy/compose/"

type composeService struct {
	Image       string            `yaml:"image"`
	User        string            `yaml:"user"`
	ReadOnly    bool              `yaml:"read_only"`
	CapDrop     []string          `yaml:"cap_drop"`
	SecurityOpt []string          `yaml:"security_opt"`
	Tmpfs       []string          `yaml:"tmpfs"`
	Ports       []string          `yaml:"ports"`
	Environment map[string]string `yaml:"environment"`
	Restart     string            `yaml:"restart"`
	Healthcheck struct {
		Test []string `yaml:"test"`
	} `yaml:"healthcheck"`
}

func readCompose(t *testing.T, file string) map[string]composeService {
	t.Helper()
	body, err := os.ReadFile(composeDir + file)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]composeService `yaml:"services"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return doc.Services
}

// Every variable the add-on sets is one the service reads.
func TestComposeAddOnNamesOnlyKnownVariables(t *testing.T) {
	known := map[string]bool{"TZ": true, "SSL_CERT_DIR": true}
	env := newServiceEnv()
	for _, name := range config.Names(&env) {
		known[name] = true
	}
	backupd := readCompose(t, "backup/backup.yml")["backupd"]
	if len(backupd.Environment) == 0 {
		t.Fatal("backupd sets no variables; this test checks nothing")
	}
	for name := range backupd.Environment {
		if !known[name] {
			t.Errorf("backup/backup.yml sets %s, which the service does not read", name)
		}
	}
}

// The repository's copy names no version; the release stamps one into
// `${BACKUP_VERSION:?…}` (#160), so every image of ours has exactly that form.
func TestComposeAddOnImagesAwaitTheReleaseVersion(t *testing.T) {
	form := regexp.MustCompile(`^\$\{BACKUP(_WEB)?_DOCKER_IMAGE:-ghcr\.io/knoppiks/opencloud-backup[a-z-]*\}:\$\{BACKUP_VERSION:\?[^}]+\}$`)
	images := 0
	for name, svc := range readCompose(t, "backup/backup.yml") {
		if svc.Image == "" {
			continue
		}
		images++
		if !form.MatchString(svc.Image) {
			t.Errorf("%s: image %q is not ${BACKUP_…_DOCKER_IMAGE:-ghcr.io/…}:${BACKUP_VERSION:?…}", name, svc.Image)
		}
	}
	if images != 2 {
		t.Fatalf("%d images, want the service and the web extension", images)
	}
}

// The service runs hardened, and nothing is published but by the
// external-proxy overlay, on localhost.
func TestComposeAddOnHardening(t *testing.T) {
	services := readCompose(t, "backup/backup.yml")
	for _, name := range []string{"backupd", "backup-vault-web"} {
		svc := services[name]
		if !svc.ReadOnly || !slices.Equal(svc.CapDrop, []string{"ALL"}) ||
			!slices.Contains(svc.SecurityOpt, "no-new-privileges:true") {
			t.Errorf("%s: want read_only, cap_drop ALL, no-new-privileges", name)
		}
		if len(svc.Ports) > 0 {
			t.Errorf("%s publishes ports in backup.yml", name)
		}
	}
	if len(services["opencloud"].Ports) > 0 {
		t.Error("backup.yml publishes OpenCloud's ports; the gateway and data server stay on the compose network")
	}

	backupd := services["backupd"]
	if backupd.User != "65532:65532" {
		t.Errorf("backupd runs as %q, want 65532:65532", backupd.User)
	}
	// Go's memory limit is derived from the container's less the tmpfs: an
	// unsized /tmp leaves it unset.
	if len(backupd.Tmpfs) != 1 || !strings.HasPrefix(backupd.Tmpfs[0], "/tmp:") || !strings.Contains(backupd.Tmpfs[0], "size=") {
		t.Errorf("backupd tmpfs = %v, want /tmp with a size", backupd.Tmpfs)
	}
	// The image has no shell: the exec form, running the subcommand.
	if !slices.Equal(backupd.Healthcheck.Test, []string{"CMD", "/backupd", "healthcheck"}) {
		t.Errorf("healthcheck = %v, want [CMD /backupd healthcheck]", backupd.Healthcheck.Test)
	} else if _, ok := lookupCommand(backupd.Healthcheck.Test[2]); !ok {
		t.Error("the healthcheck names a subcommand the binary does not have")
	}
	if got := backupd.Environment["STARTUP_WAIT_SECONDS"]; got != "0" {
		t.Errorf("STARTUP_WAIT_SECONDS = %q; Compose does not order restarts on OpenCloud's health, so wait as long as it takes", got)
	}
	if backupd.Restart == "" || backupd.Restart == "no" {
		t.Errorf("backupd restart = %q, want it restarted", backupd.Restart)
	}

	for _, port := range readCompose(t, "external-proxy/backup.yml")["backupd"].Ports {
		if !strings.HasPrefix(port, "127.0.0.1:") {
			t.Errorf("external-proxy/backup.yml publishes %q beyond localhost", port)
		}
	}
}

// .env.example offers every variable the add-on reads, and nothing it does
// not.
func TestComposeAddOnEnvExampleMatchesTheFiles(t *testing.T) {
	used := map[string]bool{}
	reference := regexp.MustCompile(`\$\{(BACKUP_[A-Z0-9_]+)`)
	for _, file := range []string{"backup/backup.yml", "traefik/backup.yml", "external-proxy/backup.yml"} {
		body, err := os.ReadFile(composeDir + file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range reference.FindAllStringSubmatch(string(body), -1) {
			used[m[1]] = true
		}
	}
	example, err := os.ReadFile(composeDir + "backup/.env.example")
	if err != nil {
		t.Fatal(err)
	}
	offered := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^#?(BACKUP_[A-Z0-9_]+)=`).FindAllStringSubmatch(string(example), -1) {
		offered[m[1]] = true
	}
	for name := range used {
		if !offered[name] {
			t.Errorf("the add-on reads %s, which backup/.env.example does not offer", name)
		}
	}
	for name := range offered {
		if !used[name] {
			t.Errorf("backup/.env.example offers %s, which the add-on does not read", name)
		}
	}
}
