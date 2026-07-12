package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvMissingFileIsOK(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "absent.env")); err != nil {
		t.Errorf("missing file should not error, got %v", err)
	}
}

func TestLoadDotEnvParsesAndPrecedence(t *testing.T) {
	content := `
# a comment
export EXPORTED_KEY=exported
QUOTED="quoted value"
SINGLE='single value'
PLAIN=plain
EXISTING=fromfile
malformed line without equals
`
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// A real env var must win over the file.
	t.Setenv("EXISTING", "fromenv")
	// Register keys the loader will set so the test harness restores them.
	for _, k := range []string{"EXPORTED_KEY", "QUOTED", "SINGLE", "PLAIN"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}

	cases := map[string]string{
		"EXPORTED_KEY": "exported",
		"QUOTED":       "quoted value",
		"SINGLE":       "single value",
		"PLAIN":        "plain",
		"EXISTING":     "fromenv", // env wins over file
	}
	for k, want := range cases {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}
