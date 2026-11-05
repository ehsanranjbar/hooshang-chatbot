package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes a config file and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const base = `
telegram:
  token: "123:SECRET-TOKEN"
  owner_id: 7
llm:
  api_key: "SECRET-KEY"
  model: "m"
`

func TestCheckListsFeaturesAndHidesSecrets(t *testing.T) {
	var out bytes.Buffer
	code := runCheck(writeConfig(t, base+"features:\n  roast:\n  glossary:\n"), &out)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "features on (2): glossary, roast") {
		t.Errorf("no feature list:\n%s", out.String())
	}
	if strings.Contains(out.String(), "SECRET") {
		t.Errorf("a secret was printed:\n%s", out.String())
	}
}

func TestCheckFailsOnABadConfig(t *testing.T) {
	var out bytes.Buffer
	if code := runCheck(writeConfig(t, base+"faal:\n  image: true\n"), &out); code != 1 || !strings.Contains(out.String(), "features.faal") {
		t.Errorf("old section: exit %d:\n%s", code, out.String())
	}
	out.Reset()
	if code := runCheck(filepath.Join(t.TempDir(), "nope.yaml"), &out); code != 1 {
		t.Errorf("missing file: exit %d", code)
	}
}

func TestCheckFailsWhenVoiceCannotRun(t *testing.T) {
	model := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(model, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cfg := base + "features:\n  voice:\n    model: " + model + "\n    whisper: /nonexistent/whisper-cli\n"
	if code := runCheck(writeConfig(t, cfg), &out); code != 1 || !strings.Contains(out.String(), "whisper-cli not found") {
		t.Errorf("exit %d:\n%s", code, out.String())
	}
}
