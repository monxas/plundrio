package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// newTestViper returns an isolated Viper instance wired exactly like `plundrio
// run` does, so the tests exercise the real env-var plumbing.
func newTestViper(t *testing.T) *viper.Viper {
	t.Helper()
	v := viper.New()
	initViperEnv(v)
	return v
}

// envNameFor mirrors the translation a shell author would expect: the flag name
// upper-cased, hyphens turned into underscores, PLDR_ prefixed.
func envNameFor(key string) string {
	return "PLDR_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}

// unsetEnv removes a variable for the duration of the test. t.Setenv records the
// original value (and whether it existed) and restores it on cleanup, so the
// following Unsetenv is undone automatically.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("could not unset %s: %v", key, err)
	}
}

// TestHyphenatedKeysResolveFromUnderscoreEnvVars pins the exact variable names
// used by the production docker-compose stack. Before SetEnvKeyReplacer every
// one of these was ignored.
func TestHyphenatedKeysResolveFromUnderscoreEnvVars(t *testing.T) {
	cases := []struct {
		key    string
		env    string
		value  string
		expect string
	}{
		{"sonarr-url", "PLDR_SONARR_URL", "http://sonarr:8989", "http://sonarr:8989"},
		{"sonarr-api-key", "PLDR_SONARR_API_KEY", "abc123", "abc123"},
		{"radarr-url", "PLDR_RADARR_URL", "http://radarr:7878", "http://radarr:7878"},
		{"radarr-api-key", "PLDR_RADARR_API_KEY", "def456", "def456"},
		{"remote-path", "PLDR_REMOTE_PATH", "/data/downloads", "/data/downloads"},
		{"local-path", "PLDR_LOCAL_PATH", "/downloads", "/downloads"},
		{"ntfy-url", "PLDR_NTFY_URL", "http://ntfy:80", "http://ntfy:80"},
		{"ntfy-topic", "PLDR_NTFY_TOPIC", "plundrio", "plundrio"},
		{"log-level", "PLDR_LOG_LEVEL", "info", "info"},
	}

	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			v := newTestViper(t)
			if got := v.GetString(tc.key); got != tc.expect {
				t.Fatalf("%s did not reach key %q: got %q, want %q", tc.env, tc.key, got, tc.expect)
			}
		})
	}
}

// TestBoolAndDurationKeysResolveFromEnv covers the auto-cancel / priority
// switches, including the ones that can cancel transfers, so a regression here
// fails loudly instead of silently disabling a safety-relevant option.
func TestBoolAndDurationKeysResolveFromEnv(t *testing.T) {
	boolCases := []struct{ key, env string }{
		{"auto-cancel-stuck", "PLDR_AUTO_CANCEL_STUCK"},
		{"auto-cancel-research", "PLDR_AUTO_CANCEL_RESEARCH"},
		{"priority-enabled", "PLDR_PRIORITY_ENABLED"},
		{"small-file-priority", "PLDR_SMALL_FILE_PRIORITY"},
		{"sequential-download", "PLDR_SEQUENTIAL_DOWNLOAD"},
		// Regression guard: this one used to need an explicit BindEnv call.
		{"disable-session-auth", "PLDR_DISABLE_SESSION_AUTH"},
	}
	for _, tc := range boolCases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, "true")
			v := newTestViper(t)
			if !v.GetBool(tc.key) {
				t.Fatalf("%s=true did not reach key %q", tc.env, tc.key)
			}
		})
	}

	durationCases := []struct {
		key, env, value string
		expect          time.Duration
	}{
		{"auto-cancel-timeout", "PLDR_AUTO_CANCEL_TIMEOUT", "12h", 12 * time.Hour},
		{"auto-cancel-min-retry", "PLDR_AUTO_CANCEL_MIN_RETRY", "1h", time.Hour},
		{"retry-base-delay", "PLDR_RETRY_BASE_DELAY", "45s", 45 * time.Second},
		{"retry-max-delay", "PLDR_RETRY_MAX_DELAY", "20m", 20 * time.Minute},
	}
	for _, tc := range durationCases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			v := newTestViper(t)
			if got := v.GetDuration(tc.key); got != tc.expect {
				t.Fatalf("%s=%s did not reach key %q: got %v, want %v", tc.env, tc.value, tc.key, got, tc.expect)
			}
		})
	}

	t.Run("PLDR_MAX_RETRIES", func(t *testing.T) {
		t.Setenv("PLDR_MAX_RETRIES", "7")
		v := newTestViper(t)
		if got := v.GetInt("max-retries"); got != 7 {
			t.Fatalf("PLDR_MAX_RETRIES=7 did not reach key \"max-retries\": got %d", got)
		}
	})
}

// TestEveryFlagIsReachableViaEnv walks the real flag set of `run`, so any flag
// added later is automatically covered and cannot regress into the
// unreachable-hyphen trap.
func TestEveryFlagIsReachableViaEnv(t *testing.T) {
	runCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Name == "config" {
			return // config file path is a flag-only concern
		}
		t.Run(envNameFor(f.Name), func(t *testing.T) {
			env := envNameFor(f.Name)
			switch f.Value.Type() {
			case "bool":
				t.Setenv(env, "true")
				v := newTestViper(t)
				if !v.GetBool(f.Name) {
					t.Fatalf("%s is not reachable from %s", f.Name, env)
				}
			case "int":
				t.Setenv(env, "37")
				v := newTestViper(t)
				if got := v.GetInt(f.Name); got != 37 {
					t.Fatalf("%s is not reachable from %s (got %d)", f.Name, env, got)
				}
			case "duration":
				t.Setenv(env, "7m")
				v := newTestViper(t)
				if got := v.GetDuration(f.Name); got != 7*time.Minute {
					t.Fatalf("%s is not reachable from %s (got %v)", f.Name, env, got)
				}
			default:
				t.Setenv(env, "sentinel-value")
				v := newTestViper(t)
				if got := v.GetString(f.Name); got != "sentinel-value" {
					t.Fatalf("%s is not reachable from %s (got %q)", f.Name, env, got)
				}
			}
		})
	})
}

// TestFlagDefaultsSurviveWithoutEnv is the other half of the contract: turning
// the replacer on must not change behaviour for anything the operator has not
// explicitly set. These are the values plundrio ran with before the fix.
func TestFlagDefaultsSurviveWithoutEnv(t *testing.T) {
	for _, env := range []string{
		"PLDR_AUTO_CANCEL_STUCK", "PLDR_AUTO_CANCEL_RESEARCH", "PLDR_AUTO_CANCEL_TIMEOUT",
		"PLDR_AUTO_CANCEL_MIN_RETRY", "PLDR_PRIORITY_ENABLED", "PLDR_SMALL_FILE_PRIORITY",
		"PLDR_SEQUENTIAL_DOWNLOAD", "PLDR_DISABLE_SESSION_AUTH", "PLDR_SONARR_URL",
		"PLDR_RADARR_URL", "PLDR_NTFY_TOPIC", "PLDR_MAX_RETRIES", "PLDR_RETRY_BASE_DELAY",
		"PLDR_RETRY_MAX_DELAY", "PLDR_WORKERS", "PLDR_LISTEN",
	} {
		unsetEnv(t, env)
	}

	v := newTestViper(t)
	if err := v.BindPFlags(runCmd.Flags()); err != nil {
		t.Fatalf("BindPFlags failed: %v", err)
	}

	if v.GetBool("auto-cancel-stuck") {
		t.Error("auto-cancel-stuck must default to false (opt-in, it deletes Put.io transfers)")
	}
	if v.GetBool("auto-cancel-research") {
		t.Error("auto-cancel-research must default to false")
	}
	if v.GetBool("priority-enabled") {
		t.Error("priority-enabled must default to false")
	}
	if v.GetBool("small-file-priority") {
		t.Error("small-file-priority must default to false")
	}
	if v.GetBool("sequential-download") {
		t.Error("sequential-download must default to false")
	}
	if v.GetBool("disable-session-auth") {
		t.Error("disable-session-auth must default to false")
	}
	if got := v.GetString("sonarr-url"); got != "" {
		t.Errorf("sonarr-url must default to empty, got %q", got)
	}
	if got := v.GetString("radarr-url"); got != "" {
		t.Errorf("radarr-url must default to empty, got %q", got)
	}
	if got := v.GetString("ntfy-topic"); got != "" {
		t.Errorf("ntfy-topic must default to empty (ntfy stays off), got %q", got)
	}
	if got := v.GetDuration("auto-cancel-timeout"); got != 12*time.Hour {
		t.Errorf("auto-cancel-timeout default changed: %v", got)
	}
	if got := v.GetDuration("auto-cancel-min-retry"); got != time.Hour {
		t.Errorf("auto-cancel-min-retry default changed: %v", got)
	}
	if got := v.GetInt("max-retries"); got != 3 {
		t.Errorf("max-retries default changed: %d", got)
	}
	if got := v.GetDuration("retry-base-delay"); got != 30*time.Second {
		t.Errorf("retry-base-delay default changed: %v", got)
	}
	if got := v.GetDuration("retry-max-delay"); got != 30*time.Minute {
		t.Errorf("retry-max-delay default changed: %v", got)
	}
	if got := v.GetInt("workers"); got != 4 {
		t.Errorf("workers default changed: %d", got)
	}
	if got := v.GetString("listen"); got != ":9091" {
		t.Errorf("listen default changed: %q", got)
	}
}

// TestEmptyEnvVarDoesNotOverrideDefault documents a behaviour we rely on: an
// env var present but empty (a common Compose artefact, e.g. ${VAR} with VAR
// unset) must not blank out a flag default.
func TestEmptyEnvVarDoesNotOverrideDefault(t *testing.T) {
	t.Setenv("PLDR_NTFY_URL", "")

	v := newTestViper(t)
	if err := v.BindPFlags(runCmd.Flags()); err != nil {
		t.Fatalf("BindPFlags failed: %v", err)
	}

	if got := v.GetString("ntfy-url"); got != "https://ntfy.sh" {
		t.Fatalf("empty PLDR_NTFY_URL clobbered the default: got %q", got)
	}
}
