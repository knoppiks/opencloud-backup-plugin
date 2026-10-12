package scheduler

import (
	"testing"
	"time"
)

func TestPresetCron(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		preset Preset
		want   string
	}{
		{"daily", Preset{Kind: PresetDaily, Hour: 2, Minute: 30}, "30 2 * * *"},
		{"daily midnight", Preset{Kind: PresetDaily}, "0 0 * * *"},
		{"weekly", Preset{Kind: PresetWeekly, Hour: 3, Minute: 0, Weekday: time.Sunday}, "0 3 * * 0"},
		{"weekly saturday", Preset{Kind: PresetWeekly, Hour: 23, Minute: 59, Weekday: time.Saturday}, "59 23 * * 6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.preset.Cron()
			if err != nil {
				t.Fatalf("Cron: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Cron = %q, want %q", got, tc.want)
			}
			if err := ValidateCron(got); err != nil {
				t.Fatalf("preset produced an invalid schedule %q: %v", got, err)
			}
		})
	}
}

func TestPresetCronRejectsNonsense(t *testing.T) {
	t.Parallel()

	invalid := []Preset{
		{Kind: PresetDaily, Hour: 24},
		{Kind: PresetDaily, Hour: -1},
		{Kind: PresetDaily, Minute: 60},
		{Kind: PresetWeekly, Weekday: 7},
		{Kind: "hourly"},
		{Kind: PresetCustom},
	}
	for _, p := range invalid {
		if _, err := p.Cron(); err == nil {
			t.Fatalf("preset %+v must be rejected", p)
		}
	}
}

// A schedule set through the UI must read back as the same preset, or the UI
// would silently degrade a user's choice to "custom" on every page load.
func TestPresetOfRoundTrips(t *testing.T) {
	t.Parallel()

	presets := []Preset{
		{Kind: PresetDaily, Hour: 2, Minute: 30},
		{Kind: PresetWeekly, Hour: 3, Minute: 15, Weekday: time.Wednesday},
	}
	for _, want := range presets {
		expr, err := want.Cron()
		if err != nil {
			t.Fatalf("Cron: %v", err)
		}
		if got := PresetOf(expr); got != want {
			t.Fatalf("PresetOf(%q) = %+v, want %+v", expr, got, want)
		}
	}
}

func TestPresetOfReportsCustomHonestly(t *testing.T) {
	t.Parallel()

	custom := []string{
		"*/15 * * * *",
		"0 2 * * 1-5",
		"0 2 1 * *",
		"not a schedule",
		"",
		"0 2 * * *  extra",
	}
	for _, expr := range custom {
		if got := PresetOf(expr); got.Kind != PresetCustom {
			t.Fatalf("PresetOf(%q) = %+v, want custom", expr, got)
		}
	}
}

func TestValidateCron(t *testing.T) {
	t.Parallel()

	valid := []string{"30 2 * * *", "0 3 * * 0", "*/10 * * * *", "0 0 1 1 *"}
	for _, expr := range valid {
		if err := ValidateCron(expr); err != nil {
			t.Fatalf("ValidateCron(%q) = %v", expr, err)
		}
	}

	invalid := []string{"", "   ", "not cron", "99 99 * * *", "* * *"}
	for _, expr := range invalid {
		if err := ValidateCron(expr); err == nil {
			t.Fatalf("ValidateCron(%q) must fail", expr)
		}
	}
}

// The error must not echo the input: schedules are user input and the message
// travels back through the API.
func TestValidateCronErrorIsGeneric(t *testing.T) {
	t.Parallel()

	err := ValidateCron("secret-looking-garbage")
	if err == nil {
		t.Fatal("want an error")
	}
	if contains(err.Error(), "secret-looking-garbage") {
		t.Fatalf("error echoes the input: %v", err)
	}
}

func TestNextAfter(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 6, 1, 0, 0, 0, time.UTC)
	got, err := NextAfter("30 2 * * *", base)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	want := time.Date(2026, 5, 6, 2, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("NextAfter = %v, want %v", got, want)
	}

	// Strictly after: firing at exactly the occurrence must yield tomorrow's.
	got, err = NextAfter("30 2 * * *", want)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	if !got.Equal(want.Add(24 * time.Hour)) {
		t.Fatalf("NextAfter at the occurrence = %v", got)
	}

	if _, err := NextAfter("nonsense", base); err == nil {
		t.Fatal("invalid expression must fail")
	}
}

// The schedule is evaluated in the configured timezone, so "nightly at 02:30"
// means the family's night.
func TestNextAfterHonoursLocation(t *testing.T) {
	t.Parallel()

	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("timezone database unavailable: %v", err)
	}

	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).In(berlin)
	got, err := NextAfter("30 2 * * *", base)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	if got.Hour() != 2 || got.Minute() != 30 {
		t.Fatalf("next run = %v, want 02:30 local", got)
	}
	// 02:30 CEST is 00:30 UTC, i.e. a different calendar hour entirely.
	if got.UTC().Hour() != 0 {
		t.Fatalf("next run in UTC = %v, want the local time to have been honoured", got.UTC())
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
