package sources

import "testing"

// clearEnv blanks every source credential so each test opts in explicitly.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HARDCOVER_TOKEN", "GRIMMORY_URL", "GRIMMORY_USERNAME", "GRIMMORY_PASSWORD"} {
		t.Setenv(key, "")
	}
}

func TestFromEnvNoneConfigured(t *testing.T) {
	clearEnv(t)

	if got := FromEnv(); got != nil {
		t.Errorf("FromEnv() = %v, want nil when nothing is configured", got)
	}
}

func TestFromEnvEnablesHardcover(t *testing.T) {
	clearEnv(t)
	t.Setenv("HARDCOVER_TOKEN", "tok")

	got := FromEnv()
	if got == nil {
		t.Fatal("FromEnv() = nil, want a source when HARDCOVER_TOKEN is set")
	}
	if got.Name() != "hardcover" {
		t.Errorf("Name() = %q, want hardcover", got.Name())
	}
}

func TestFromEnvEnablesGrimmory(t *testing.T) {
	clearEnv(t)
	t.Setenv("GRIMMORY_URL", "http://gm.local:6060")
	t.Setenv("GRIMMORY_USERNAME", "user")
	t.Setenv("GRIMMORY_PASSWORD", "pass")

	got := FromEnv()
	if got == nil {
		t.Fatal("FromEnv() = nil, want a source when Grimmory is configured")
	}
	if got.Name() != "grimmory" {
		t.Errorf("Name() = %q, want grimmory", got.Name())
	}
}

func TestFromEnvCombinesBackends(t *testing.T) {
	clearEnv(t)
	t.Setenv("HARDCOVER_TOKEN", "tok")
	t.Setenv("GRIMMORY_URL", "http://gm.local:6060")
	t.Setenv("GRIMMORY_USERNAME", "user")
	t.Setenv("GRIMMORY_PASSWORD", "pass")

	got := FromEnv()
	if got == nil {
		t.Fatal("FromEnv() = nil, want a combined source")
	}
	if got.Name() != "hardcover+grimmory" {
		t.Errorf("Name() = %q, want hardcover+grimmory", got.Name())
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

			if got := FromEnv(); got != nil {
				t.Errorf("FromEnv() = %v, want nil for a URL without full credentials", got)
			}
		})
	}
}
