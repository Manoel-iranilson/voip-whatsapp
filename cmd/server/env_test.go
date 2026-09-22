package main

import (
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/store"
)

// envMap stands in for os.LookupEnv.
func envMap(pairs map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := pairs[name]
		return v, ok
	}
}

// testFlags mirrors the real flag set closely enough to exercise the mapping.
func testFlags() (*flag.FlagSet, *string, *int, *bool, *repeatedFlag) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addr := fs.String("addr", "127.0.0.1:8080", "")
	maxCalls := fs.Int("max-calls-per-session", 8, "")
	recordAll := fs.Bool("record-all", false, "")
	var keys repeatedFlag
	fs.Var(&keys, "api-key", "")
	return fs, addr, maxCalls, recordAll, &keys
}

func TestEnvFillsUnsetFlags(t *testing.T) {
	fs, addr, maxCalls, recordAll, _ := testFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}

	err := applyEnvDefaults(fs, envMap(map[string]string{
		"WACALLS_ADDR":                  ":9000",
		"WACALLS_MAX_CALLS_PER_SESSION": "3",
		"WACALLS_RECORD_ALL":            "true",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if *addr != ":9000" {
		t.Fatalf("addr = %q, want :9000", *addr)
	}
	if *maxCalls != 3 {
		t.Fatalf("max calls = %d, want 3", *maxCalls)
	}
	if !*recordAll {
		t.Fatal("record-all should have been turned on by the environment")
	}
}

// Anyone already running this with flags must keep the behaviour they have.
func TestFlagBeatsEnv(t *testing.T) {
	fs, addr, _, _, _ := testFlags()
	if err := fs.Parse([]string{"-addr", ":7777"}); err != nil {
		t.Fatal(err)
	}

	if err := applyEnvDefaults(fs, envMap(map[string]string{"WACALLS_ADDR": ":9000"})); err != nil {
		t.Fatal(err)
	}
	if *addr != ":7777" {
		t.Fatalf("addr = %q, want the flag's :7777 to win over the environment", *addr)
	}
}

// A flag left at its default and absent from the environment stays put.
func TestEnvLeavesDefaultsAlone(t *testing.T) {
	fs, addr, maxCalls, _, _ := testFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if err := applyEnvDefaults(fs, envMap(nil)); err != nil {
		t.Fatal(err)
	}
	if *addr != "127.0.0.1:8080" || *maxCalls != 8 {
		t.Fatalf("defaults changed: addr=%q maxCalls=%d", *addr, *maxCalls)
	}
}

// An empty variable is treated as "not set" rather than as an empty value, so an
// unfilled Portainer field cannot silently blank a setting.
func TestEnvIgnoresEmptyValues(t *testing.T) {
	fs, addr, _, _, _ := testFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if err := applyEnvDefaults(fs, envMap(map[string]string{"WACALLS_ADDR": ""})); err != nil {
		t.Fatal(err)
	}
	if *addr != "127.0.0.1:8080" {
		t.Fatalf("addr = %q, want the default untouched", *addr)
	}
}

// The environment cannot repeat a key, so a repeatable flag takes a list.
func TestEnvRepeatableFlagTakesAList(t *testing.T) {
	fs, _, _, _, keys := testFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if err := applyEnvDefaults(fs, envMap(map[string]string{
		"WACALLS_API_KEY": "crm:secret-one, n8n:secret-two ",
	})); err != nil {
		t.Fatal(err)
	}

	parsed, err := parseAPIKeys(*keys)
	if err != nil {
		t.Fatal(err)
	}
	if parsed["crm"] != "secret-one" || parsed["n8n"] != "secret-two" {
		t.Fatalf("parsed %v, want both keys with their secrets trimmed", parsed)
	}
}

func TestEnvReportsBadValues(t *testing.T) {
	fs, _, _, _, _ := testFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	err := applyEnvDefaults(fs, envMap(map[string]string{"WACALLS_MAX_CALLS_PER_SESSION": "banana"}))
	if err == nil {
		t.Fatal("a value that does not parse must be reported, not ignored")
	}
	if !strings.Contains(err.Error(), "WACALLS_MAX_CALLS_PER_SESSION") || !strings.Contains(err.Error(), "banana") {
		t.Fatalf("error should name the variable and the value, got %q", err)
	}
}

// What WhatsApp lists under "Linked devices" is store.DeviceProps.Os, and the
// default is the library's own name. This asserts the value that actually goes
// out in the registration payload.
func TestApplyDeviceName(t *testing.T) {
	original := store.DeviceProps.GetOs()
	t.Cleanup(func() { store.SetOSInfo(original, deviceVersion) })

	if err := applyDeviceName("ORBSDR"); err != nil {
		t.Fatal(err)
	}
	if got := store.DeviceProps.GetOs(); got != "ORBSDR" {
		t.Fatalf("device name = %q, want ORBSDR", got)
	}

	if err := applyDeviceName("  Padded Name  "); err != nil {
		t.Fatal(err)
	}
	if got := store.DeviceProps.GetOs(); got != "Padded Name" {
		t.Fatalf("device name = %q, want it trimmed", got)
	}
}

// An empty name would pair accounts under a blank label, which is worse than the
// default and takes another re-pairing to undo.
func TestApplyDeviceNameRejectsEmpty(t *testing.T) {
	original := store.DeviceProps.GetOs()
	t.Cleanup(func() { store.SetOSInfo(original, deviceVersion) })

	if err := applyDeviceName("ORBSDR"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "   ", "\t\n"} {
		if err := applyDeviceName(bad); err == nil {
			t.Fatalf("%q should have been rejected", bad)
		}
		if got := store.DeviceProps.GetOs(); got != "ORBSDR" {
			t.Fatalf("a rejected name still changed the value to %q", got)
		}
	}
}

// The default is the product's name, not the library's.
func TestDefaultDeviceNameIsNotTheLibrary(t *testing.T) {
	if defaultDeviceName == "whatsmeow" || defaultDeviceName == "" {
		t.Fatalf("default device name is %q", defaultDeviceName)
	}
}

func TestDeviceNameFromEnv(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("device-name", defaultDeviceName, "")
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if err := applyEnvDefaults(fs, envMap(map[string]string{"WACALLS_DEVICE_NAME": "OutraMarca"})); err != nil {
		t.Fatal(err)
	}
	if *name != "OutraMarca" {
		t.Fatalf("device name = %q, want the environment value", *name)
	}
}

// A container healthcheck arrives with no credential. If this needed one it would
// always get 401 and the orchestrator would keep restarting a healthy service.
func TestHealthzNeedsNoCredential(t *testing.T) {
	srv := newTestServerWithConfig(t, config{
		token:   "operator-token",
		apiKeys: apiKeys{"crm": "crm-secret"},
	})
	h := srv.routes()

	res := do(t, h, "GET", "http://example.com/healthz", nil, "")
	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 without any credential", res.Code)
	}

	// Being public, it must carry nothing but liveness.
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["status"] != "ok" {
		t.Fatalf(`healthz should answer exactly {"status":"ok"}, got %v`, body)
	}
	if raw := res.Body.String(); strings.Contains(raw, "secret") || strings.Contains(raw, "token") {
		t.Fatalf("healthz leaked configuration: %s", raw)
	}

	// /api stays guarded.
	if res := do(t, h, "GET", "http://example.com/api/sessions", nil, ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("/api without a credential: got %d, want 401", res.Code)
	}
}
