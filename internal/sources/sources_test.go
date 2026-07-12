package sources

import "testing"

func TestFromEnvNoneConfigured(t *testing.T) {
	t.Setenv("HARDCOVER_TOKEN", "")

	if got := FromEnv(); got != nil {
		t.Errorf("FromEnv() = %v, want nil when nothing is configured", got)
	}
}

func TestFromEnvEnablesHardcover(t *testing.T) {
	t.Setenv("HARDCOVER_TOKEN", "tok")

	got := FromEnv()
	if got == nil {
		t.Fatal("FromEnv() = nil, want a source when HARDCOVER_TOKEN is set")
	}
	if got.Name() != "hardcover" {
		t.Errorf("Name() = %q, want hardcover", got.Name())
	}
}
