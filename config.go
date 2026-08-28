package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Config is the on-disk settings file. Every field is optional; a missing one
// falls back to defaultConfig().
type Config struct {
	MusicDir        string   `json:"music_dir"`
	RedisAddr       string   `json:"redis_addr"`
	SpotifyClientID string   `json:"spotify_client_id"`
	DeviceName      string   `json:"device_name"`
	Bitrate         int      `json:"bitrate"`
	Volume          int      `json:"volume"`
	EQ              EQConfig `json:"eq"`

	// SpotifyBackend is "soloist" or "librespot". Soloist is Spotify's own
	// headless client and streams reliably; librespot is refused audio keys on
	// newer accounts (librespot#1649). Soloist is Linux-only, so on macOS it
	// runs in Docker - see docker/.
	SpotifyBackend string `json:"spotify_backend"`

	// SoloistImage is the container image built from docker/Dockerfile.
	SoloistImage string `json:"soloist_image"`

	// Lossless captures Soloist's output at 32 bits instead of 16. Spotify's
	// lossless tier carries more than 16 bits, and a 16-bit capture would throw
	// that away before SEHO ever saw it.
	Lossless bool `json:"lossless"`
}

// EQConfig records which profile is selected and, when the user has edited its
// curve, the modified bands. Bands empty means "use the profile as published".
type EQConfig struct {
	Enabled bool   `json:"enabled"`
	Profile string `json:"profile"`
	Bands   []band `json:"bands,omitempty"`
}

// Settings separates what is on disk from what the process actually uses.
//
// Environment variables win over the file, but Save must not bake an env value
// into the file - unsetting the variable later would then silently resurrect it
// as a stored setting. So File is what Save writes, Eff is what the app reads,
// and Env records which fields the environment took over so the settings page
// can render those read-only instead of pretending they are editable.
type Settings struct {
	File Config
	Eff  Config
	Env  map[string]string // config field name -> env var that overrode it
}

func defaultConfig() Config {
	return Config{
		MusicDir:       defaultMusicDir(),
		RedisAddr:      "127.0.0.1:6379",
		DeviceName:     "SEHO",
		Bitrate:        320,
		Volume:         100,
		EQ:             EQConfig{Enabled: true, Profile: defaultProfileName()},
		SpotifyBackend: backendSoloist,
		SoloistImage:   "seho-soloist:latest",
		Lossless:       true,
	}
}

// configDir is ~/.config/seho, or ./.seho when there is no home directory.
func configDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".seho"
	}
	return filepath.Join(home, ".config", "seho")
}

func configPath() string { return filepath.Join(configDir(), "config.json") }

// expandTilde resolves a leading ~ so a hand-edited config file behaves the
// way the shell would.
func expandTilde(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
}

// loadSettings reads the config file, applies environment overrides, and
// reports which fields the environment claimed. A missing or corrupt file is
// not an error: defaults are a working configuration, and refusing to start
// over a stray character in a settings file would be worse than ignoring it.
func loadSettings() Settings {
	file := defaultConfig()
	if b, err := os.ReadFile(configPath()); err == nil {
		// Unmarshal over the defaults so absent keys keep them.
		if err := json.Unmarshal(b, &file); err != nil {
			file = defaultConfig()
		}
	}
	return applyEnv(file, os.Getenv)
}

// applyEnv is the pure half of loadSettings, so precedence is testable without
// touching the filesystem.
func applyEnv(file Config, getenv func(string) string) Settings {
	s := Settings{File: file, Eff: file, Env: map[string]string{}}
	s.Eff.MusicDir = expandTilde(s.Eff.MusicDir)
	s.File.MusicDir = file.MusicDir

	if v := getenv("MUSIC_DIR"); v != "" {
		s.Eff.MusicDir = expandTilde(v)
		s.Env["music_dir"] = "MUSIC_DIR"
	}
	if v := getenv("REDIS_ADDR"); v != "" {
		s.Eff.RedisAddr = v
		s.Env["redis_addr"] = "REDIS_ADDR"
	}
	if v := getenv("SPOTIFY_CLIENT_ID"); v != "" {
		s.Eff.SpotifyClientID = v
		s.Env["spotify_client_id"] = "SPOTIFY_CLIENT_ID"
	}
	if v := getenv("SEHO_DEVICE_NAME"); v != "" {
		s.Eff.DeviceName = v
		s.Env["device_name"] = "SEHO_DEVICE_NAME"
	}
	if v := getenv("SEHO_SPOTIFY_BACKEND"); v != "" {
		s.Eff.SpotifyBackend = v
		s.Env["spotify_backend"] = "SEHO_SPOTIFY_BACKEND"
	}
	if v := getenv("SEHO_BITRATE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			s.Eff.Bitrate = n
			s.Env["bitrate"] = "SEHO_BITRATE"
		}
	}
	return s
}

// Save writes the file half, leaving env-overridden fields as they were on
// disk. 0644 is deliberate: nothing secret lives here (the client id is public
// by design), and the refresh token goes to the keychain instead.
func (s *Settings) Save() error {
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.File, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), append(b, '\n'), 0o644)
}

// --- refresh token storage -------------------------------------------------

const (
	keychainService = "seho"
	keychainAccount = "spotify-refresh"
	// The Soloist developer API key is a credential too, so it lives beside the
	// refresh token rather than in the plain config file.
	keychainSoloist = "soloist-api-key"
)

// Backend names for Config.SpotifyBackend.
const (
	backendSoloist   = "soloist"
	backendLibrespot = "librespot"
)

func tokenFilePath() string { return filepath.Join(configDir(), "token.json") }

// saveRefreshToken stores the token in the login keychain on macOS, falling
// back to a 0600 file when the keychain is unavailable (every other platform,
// a locked keychain, or a stripped-down macOS install).
//
// ponytail: the token is passed on argv, which is visible to other processes
// running as this same user via ps. macOS does not expose one user's argv to
// another, and `security` offers no stdin path for -w, so this is the ceiling
// of the CLI approach. Move to the Security framework via cgo if that ever
// stops being acceptable.
func saveRefreshToken(tok string) error {
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("security", "add-generic-password",
			"-s", keychainService, "-a", keychainAccount, "-w", tok, "-U")
		if err := cmd.Run(); err == nil {
			// Remove any stale fallback file so two stores cannot disagree.
			os.Remove(tokenFilePath())
			return nil
		}
	}
	return saveTokenFile(tok)
}

func saveTokenFile(tok string) error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(map[string]string{"refresh_token": tok})
	if err != nil {
		return err
	}
	return os.WriteFile(tokenFilePath(), b, 0o600)
}

// SaveSoloistKey stores the Soloist developer API key.
func SaveSoloistKey(key string) error { return saveSecret(keychainSoloist, key, soloistKeyPath()) }

// LoadSoloistKey returns the stored Soloist key, preferring the keychain and
// falling back to a 0600 file. SOLOIST_API_KEY overrides both, so a key can be
// supplied for one run without storing it anywhere.
func LoadSoloistKey() string {
	if v := os.Getenv("SOLOIST_API_KEY"); v != "" {
		return v
	}
	tok, _ := loadSecret(keychainSoloist, soloistKeyPath())
	return tok
}

func soloistKeyPath() string { return filepath.Join(configDir(), "soloist-key.json") }

// saveSecret and loadSecret are the generic halves of the token storage above,
// shared by the refresh token and the Soloist key.
func saveSecret(account, value, fallback string) error {
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("security", "add-generic-password",
			"-s", keychainService, "-a", account, "-w", value, "-U")
		if err := cmd.Run(); err == nil {
			os.Remove(fallback)
			return nil
		}
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(map[string]string{"value": value})
	if err != nil {
		return err
	}
	return os.WriteFile(fallback, b, 0o600)
}

func loadSecret(account, fallback string) (string, bool) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("security", "find-generic-password",
			"-s", keychainService, "-a", account, "-w").Output()
		if err == nil {
			if t := strings.TrimSpace(string(out)); t != "" {
				return t, true
			}
		}
	}
	b, err := os.ReadFile(fallback)
	if err != nil {
		return "", false
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return "", false
	}
	return m["value"], false
}

// loadRefreshToken returns the stored token and whether it came from the
// keychain, so the settings page can say where the credential lives.
func loadRefreshToken() (tok string, fromKeychain bool) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("security", "find-generic-password",
			"-s", keychainService, "-a", keychainAccount, "-w").Output()
		if err == nil {
			if t := strings.TrimSpace(string(out)); t != "" {
				return t, true
			}
		}
	}
	b, err := os.ReadFile(tokenFilePath())
	if err != nil {
		return "", false
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return "", false
	}
	return m["refresh_token"], false
}

// clearRefreshToken forgets the credential from both stores. Errors from an
// absent entry are not failures - the desired end state is "no token".
func clearRefreshToken() error {
	if runtime.GOOS == "darwin" {
		exec.Command("security", "delete-generic-password",
			"-s", keychainService, "-a", keychainAccount).Run()
	}
	if err := os.Remove(tokenFilePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// --- hardware detection ----------------------------------------------------

// macModel returns this machine's model identifier (e.g. "Mac16,1"), or "" off
// macOS or when sysctl is unavailable.
func macModel() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("sysctl", "-n", "hw.model").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
