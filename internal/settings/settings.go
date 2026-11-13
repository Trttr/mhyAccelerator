// Package settings stores the user's choices (language, games) shared by the
// console and windowed versions, in %LOCALAPPDATA%\mhyAccelerator\settings.json.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Settings persist between runs.
type Settings struct {
	Lang  string   `json:"lang"`  // "zh" or "en"
	Games []string `json:"games"` // last selection (biz ids)
}

// Dir is the app's data folder.
func Dir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "mhyAccelerator")
}

// Path is the settings file.
func Path() string { return filepath.Join(Dir(), "settings.json") }

// Load reads the settings; missing or corrupt files give zero values.
func Load() Settings {
	var s Settings
	if data, err := os.ReadFile(Path()); err == nil {
		json.Unmarshal(data, &s)
	}
	return s
}

// Save writes the settings.
func Save(s Settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(Path(), data, 0o644)
}
