package sources

import (
	"context"
	"errors"
	"testing"

	"nextleaf/internal/library"
)

// clearEnv blanks every source credential so each test opts in explicitly.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HARDCOVER_TOKEN", "GRIMMORY_URL", "GRIMMORY_USERNAME", "GRIMMORY_PASSWORD"} {
		t.Setenv(key, "")
	}
}

func TestFromEnvNoneConfigured(t *testing.T) {
	clearEnv(t)

	got, enabled := FromEnv()
	if got != nil {
		t.Errorf("FromEnv() = %v, want nil when nothing is configured", got)
	}
	if len(enabled) != 0 {
		t.Errorf("enabled = %v, want none", enabled)
	}
}

func TestFromEnvEnablesHardcover(t *testing.T) {
	clearEnv(t)
	t.Setenv("HARDCOVER_TOKEN", "tok")

	got, enabled := FromEnv()
	if got == nil {
		t.Fatal("FromEnv() = nil, want a source when HARDCOVER_TOKEN is set")
	}
	if got.Name() != "hardcover" {
		t.Errorf("Name() = %q, want hardcover", got.Name())
	}
	if names := sourceNames(enabled); len(names) != 1 || names[0] != "hardcover" {
		t.Errorf("enabled = %v, want [hardcover]", names)
	}
}

func TestFromEnvEnablesGrimmory(t *testing.T) {
	clearEnv(t)
	t.Setenv("GRIMMORY_URL", "http://gm.local:6060")
	t.Setenv("GRIMMORY_USERNAME", "user")
	t.Setenv("GRIMMORY_PASSWORD", "pass")

	got, enabled := FromEnv()
	if got == nil {
		t.Fatal("FromEnv() = nil, want a source when Grimmory is configured")
	}
	if got.Name() != "grimmory" {
		t.Errorf("Name() = %q, want grimmory", got.Name())
	}
	if names := sourceNames(enabled); len(names) != 1 || names[0] != "grimmory" {
		t.Errorf("enabled = %v, want [grimmory]", names)
	}
}

func TestFromEnvCombinesBackends(t *testing.T) {
	clearEnv(t)
	t.Setenv("HARDCOVER_TOKEN", "tok")
	t.Setenv("GRIMMORY_URL", "http://gm.local:6060")
	t.Setenv("GRIMMORY_USERNAME", "user")
	t.Setenv("GRIMMORY_PASSWORD", "pass")

	got, enabled := FromEnv()
	if got == nil {
		t.Fatal("FromEnv() = nil, want a combined source")
	}
	if got.Name() != "hardcover+grimmory" {
		t.Errorf("Name() = %q, want hardcover+grimmory", got.Name())
	}
	if names := sourceNames(enabled); len(names) != 2 || names[0] != "hardcover" || names[1] != "grimmory" {
		t.Errorf("enabled = %v, want [hardcover grimmory]", names)
	}
}

func TestFromEnvGrimmoryNeedsCredentials(t *testing.T) {
	cases := map[string][2]string{
		"missing password": {"user", ""},
		"missing username": {"", "pass"},
	}
	for name, creds := range cases {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("GRIMMORY_URL", "http://gm.local:6060")
			t.Setenv("GRIMMORY_USERNAME", creds[0])
			t.Setenv("GRIMMORY_PASSWORD", creds[1])

			if got, enabled := FromEnv(); got != nil || len(enabled) != 0 {
				t.Errorf("FromEnv() = (%v, %v), want (nil, none) for a URL without full credentials", got, enabled)
			}
		})
	}
}

func sourceNames(srcs []library.Source) []string {
	names := make([]string, len(srcs))
	for i, s := range srcs {
		names[i] = s.Name()
	}
	return names
}

// verifiableSource is a Source that reports a verification result.
type verifiableSource struct {
	name string
	err  error
}

func (v verifiableSource) Name() string                                            { return v.name }
func (verifiableSource) CurrentlyReading(context.Context) ([]library.Entry, error) { return nil, nil }
func (verifiableSource) RecentReads(context.Context, int) ([]library.Entry, error) { return nil, nil }
func (verifiableSource) ToRead(context.Context) ([]library.Entry, error)           { return nil, nil }
func (v verifiableSource) Verify(context.Context) error                            { return v.err }

func TestVerify(t *testing.T) {
	authErr := errors.New("unauthorized")
	srcs := []library.Source{
		verifiableSource{name: "ok"},
		verifiableSource{name: "bad", err: authErr},
	}

	reports := Verify(context.Background(), srcs)
	if len(reports) != 2 {
		t.Fatalf("len(reports) = %d, want 2", len(reports))
	}
	if reports[0].Name != "ok" || reports[0].Err != nil {
		t.Errorf("reports[0] = %+v, want {ok <nil>}", reports[0])
	}
	if reports[1].Name != "bad" || !errors.Is(reports[1].Err, authErr) {
		t.Errorf("reports[1] = %+v, want bad with the auth error", reports[1])
	}
}

func TestVerifyUnsupportedSourceSkips(t *testing.T) {
	// A source that cannot verify reports a nil error rather than being probed.
	reports := Verify(context.Background(), []library.Source{plainSource{name: "plain"}})
	if len(reports) != 1 || reports[0].Name != "plain" || reports[0].Err != nil {
		t.Errorf("reports = %+v, want one {plain <nil>}", reports)
	}
}

// plainSource has no Verify method at all.
type plainSource struct{ name string }

func (p plainSource) Name() string                                            { return p.name }
func (plainSource) CurrentlyReading(context.Context) ([]library.Entry, error) { return nil, nil }
func (plainSource) RecentReads(context.Context, int) ([]library.Entry, error) { return nil, nil }
func (plainSource) ToRead(context.Context) ([]library.Entry, error)           { return nil, nil }
