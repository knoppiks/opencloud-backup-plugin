package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// listener and sample stand in for a component's section and a binary's root.
type listener struct {
	Addr string   `env:"T_ADDR" doc:"Address."`
	Path string   `env:"T_PATH" doc:"Path prefix."`
	Tags []string `env:"T_TAGS" doc:"Tags."`

	// calls records the validation order across sections.
	calls *[]string
}

func (l *listener) Validate() error {
	*l.calls = append(*l.calls, "listener")
	l.Path = strings.TrimRight(l.Path, "/")
	if strings.Contains(l.Path, "://") {
		return errors.New("T_PATH must be a path")
	}
	return nil
}

type tuning struct {
	Enable  bool   `env:"T_ENABLE" doc:"Switch."`
	Workers int    `env:"T_WORKERS" doc:"Workers."`
	Backend string `env:"T_BACKEND" values:"memory,disk" doc:"Backend."`
	Token   Secret `env:"T_TOKEN" doc:"Token."`
}

type sample struct {
	Listener listener `section:"Listener"`
	Tuning   tuning   `section:"Tuning"`

	calls []string
}

func (s *sample) Validate() error {
	s.calls = append(s.calls, "root")
	// A cross-section check sees the sections normalised.
	if s.Tuning.Enable && s.Listener.Path == "" {
		return errors.New("T_ENABLE needs T_PATH")
	}
	return nil
}

// newSample holds the defaults, as a component's constructor would.
func newSample() *sample {
	s := &sample{Listener: listener{Addr: ":8080"}, Tuning: tuning{Workers: 2}}
	s.Listener.calls = &s.calls
	return s
}

func load(t *testing.T, environ ...string) *sample {
	t.Helper()
	s := newSample()
	if err := Load(environ, s); err != nil {
		t.Fatalf("Load(%v): %v", environ, err)
	}
	return s
}

func loadErr(t *testing.T, environ ...string) error {
	t.Helper()
	err := Load(environ, newSample())
	if err == nil {
		t.Fatalf("Load(%v) succeeded, want an error", environ)
	}
	return err
}

func TestLoad_UnsetAndEmptyKeepTheDefaults(t *testing.T) {
	for _, environ := range [][]string{nil, {"T_ADDR=", "T_WORKERS=  ", "T_ENABLE="}} {
		s := load(t, environ...)
		if s.Listener.Addr != ":8080" || s.Tuning.Workers != 2 || s.Tuning.Enable {
			t.Errorf("%v: %+v", environ, s)
		}
		if s.Tuning.Token.IsSet() {
			t.Error("an unset secret reads as set")
		}
	}
}

func TestLoad_ReadsValues(t *testing.T) {
	s := load(t,
		"T_ADDR= :9090 ",
		"T_TAGS=a, b,,c ",
		"T_ENABLE=true", "T_PATH=/x/",
		"T_WORKERS=4",
		"T_BACKEND=Memory",
		"T_TOKEN= keeps its spaces ",
	)
	if s.Listener.Addr != ":9090" {
		t.Errorf("Addr = %q, want it trimmed", s.Listener.Addr)
	}
	if !reflect.DeepEqual(s.Listener.Tags, []string{"a", "b", "c"}) {
		t.Errorf("Tags = %q", s.Listener.Tags)
	}
	if !s.Tuning.Enable || s.Tuning.Workers != 4 {
		t.Errorf("Tuning = %+v", s.Tuning)
	}
	if s.Tuning.Backend != "memory" {
		t.Errorf("Backend = %q, want the declared spelling", s.Tuning.Backend)
	}
	// A password may legitimately start or end with a space; a secret is
	// taken as given.
	if s.Tuning.Token.Reveal() != " keeps its spaces " {
		t.Error("the secret was altered")
	}
}

// os.Getenv returns the first of duplicate entries; so does the loader, so a
// value is the same whichever way it is read.
func TestLoad_FirstDuplicateWins(t *testing.T) {
	if s := load(t, "T_ADDR=:1", "T_ADDR=:2"); s.Listener.Addr != ":1" {
		t.Fatalf("Addr = %q, want the first entry", s.Listener.Addr)
	}
}

// "yes" used to mean false without a word. A typo in a switch must be refused,
// not read as "off" (review-2026-10.md G4).
func TestLoad_BooleansAreStrict(t *testing.T) {
	for _, value := range []string{"yes", "no", "1", "0", "True", "TRUE", "on", "t"} {
		if err := loadErr(t, "T_ENABLE="+value); !strings.Contains(err.Error(), "T_ENABLE must be true or false") {
			t.Errorf("%q: err = %v", value, err)
		}
	}
	if !load(t, "T_ENABLE= true ", "T_PATH=/x").Tuning.Enable {
		t.Error("true is not true")
	}
	if load(t, "T_ENABLE=false").Tuning.Enable {
		t.Error("false is not false")
	}
}

func TestLoad_IntegersAreNonNegative(t *testing.T) {
	for _, value := range []string{"-1", "abc", "1.5", "2k"} {
		if err := loadErr(t, "T_WORKERS="+value); !strings.Contains(err.Error(), "T_WORKERS must be a non-negative integer") {
			t.Errorf("%q: err = %v", value, err)
		}
	}
}

func TestLoad_RestrictedValues(t *testing.T) {
	if err := loadErr(t, "T_BACKEND=mem"); !strings.Contains(err.Error(), "T_BACKEND must be one of memory, disk") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoad_SecretsAreNeverQuotedInErrors(t *testing.T) {
	type broken struct {
		Token Secret `env:"T_TOKEN" doc:"Token."`
	}
	value := randomValue(t)
	err := Load([]string{"T_TOKEN=" + PlaceholderMarker + value}, &broken{})
	if err == nil || strings.Contains(err.Error(), value) {
		t.Fatalf("err = %v", err)
	}
}

// One restart shows every problem, type errors and checks alike.
func TestLoad_ReportsEveryProblem(t *testing.T) {
	err := loadErr(t, "T_WORKERS=x", "T_BACKEND=nope", "T_PATH=https://x", "T_ENABLE=true")
	for _, want := range []string{"T_WORKERS", "T_BACKEND", "T_PATH must be a path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

// Sections are validated before the struct holding them, so a check that
// spans sections sees normalised values.
func TestLoad_ValidatesSectionsThenTheRoot(t *testing.T) {
	s := load(t, "T_PATH=/x/")
	if !reflect.DeepEqual(s.calls, []string{"listener", "root"}) {
		t.Fatalf("calls = %v", s.calls)
	}
	if s.Listener.Path != "/x" {
		t.Fatalf("Path = %q, want the section's normalisation", s.Listener.Path)
	}
	if err := loadErr(t, "T_ENABLE=true", "T_PATH=/"); !strings.Contains(err.Error(), "T_ENABLE needs T_PATH") {
		t.Fatalf("err = %v, want the root's check to see the normalised path", err)
	}
}

func TestLoad_PlaceholdersAreReportedTogetherAndAlone(t *testing.T) {
	err := loadErr(t, "T_ADDR=REPLACE_ME_ADDR", "T_PATH=REPLACE_ME", "T_WORKERS=not-a-number")
	if !errors.Is(err, ErrPlaceholder) {
		t.Fatalf("err = %v", err)
	}
	for _, name := range []string{"T_ADDR", "T_PATH"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s", name)
		}
	}
	// Every diagnosis after an unfinished manifest is of a symptom.
	if strings.Contains(err.Error(), "T_WORKERS") {
		t.Errorf("the placeholder error is buried among others: %v", err)
	}
}

// Somebody else's placeholder is somebody else's problem: refusing to start
// over a variable this program never reads would be a surprise with no fix
// inside this deployment.
func TestLoad_IgnoresForeignPlaceholders(t *testing.T) {
	load(t, "SOME_OTHER_CHART_TOKEN=REPLACE_ME", "T_SOMETHING_ELSE=REPLACE_ME")
}

func TestLoad_RefusesWhatIsNotAPointerToAStruct(t *testing.T) {
	for name, root := range map[string]any{"value": sample{}, "nil": nil, "pointer to int": new(int)} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()
			_ = Load(nil, root)
		})
	}
}

// A field without an env tag is a declaration mistake, found by the first
// test that loads the struct rather than by an operator.
func TestLoad_RefusesAnUndeclaredField(t *testing.T) {
	type undeclared struct{ Addr string }
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	_ = Load(nil, &undeclared{})
}
