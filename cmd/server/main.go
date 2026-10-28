package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow/store"
)

func main() {
	// Loopback by default: the API has no per-user authentication, so exposing it
	// on every interface has to be a deliberate choice (-addr :8080).
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	dbPath := flag.String("db", "wacalls.db", "SQLite session database path")
	staticDir := flag.String("static", "client/dist", "static client directory (optional)")
	allowOrigin := flag.String("allow-origin", "", "comma-separated browser origins allowed to call the API ('*' allows any; empty = same-origin only)")
	token := flag.String("token", "", "shared token required on /api (empty = no token check)")
	deviceName := flag.String("device-name", defaultDeviceName, "name shown in WhatsApp under Linked devices (applies to new pairings only)")
	var keyFlags repeatedFlag
	flag.Var(&keyFlags, "api-key", `credential for another system, as "name:secret" (repeatable)`)
	webhookURL := flag.String("webhook-url", "", "URL to POST call events to (empty = no webhooks)")
	webhookSecret := flag.String("webhook-secret", "", "secret used to sign webhook bodies (HMAC-SHA256)")
	publicIP := flag.String("public-ip", "", "public IP advertised to browsers; required when running behind NAT, such as in a container on a bridge network")
	udpPortMin := flag.Int("udp-port-min", 0, "lowest UDP port for browser media (0 = any free port)")
	udpPortMax := flag.Int("udp-port-max", 0, "highest UDP port for browser media (0 = any free port)")
	recordDir := flag.String("record-dir", "", "directory for call recordings (empty = recording unavailable)")
	recordAll := flag.Bool("record-all", false, "record every call, including inbound ones (requires -record-dir)")
	debug := flag.Bool("debug", false, "verbose logging")
	maxCalls := flag.Int("max-calls-per-session", 8, "max concurrent calls per session (0 = unlimited)")
	flag.Parse()
	if err := applyEnvDefaults(flag.CommandLine, os.LookupEnv); err != nil {
		fmt.Fprintln(os.Stderr, "bad environment variable:", err)
		os.Exit(1)
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := config{
		addr:        *addr,
		dbPath:      *dbPath,
		staticDir:   *staticDir,
		allowOrigin: *allowOrigin,
		token:       *token,
		maxCalls:    *maxCalls,
		recordDir:   *recordDir,
		recordAll:   *recordAll,

		webhookURL:    *webhookURL,
		webhookSecret: *webhookSecret,

		ice: iceConfig{
			PublicIP:   *publicIP,
			UDPPortMin: *udpPortMin,
			UDPPortMax: *udpPortMax,
		},
	}

	keys, err := parseAPIKeys(keyFlags)
	if err != nil {
		log.Error("bad -api-key", "err", err)
		os.Exit(1)
	}
	cfg.apiKeys = keys

	// Before any client exists: store.DeviceProps is global and is read when the
	// pairing connection is built, so setting it later would be a race that pairs
	// an account under the old name depending on timing.
	if err := applyDeviceName(*deviceName); err != nil {
		log.Error("bad -device-name", "err", err)
		os.Exit(1)
	}

	// Also before any client exists: newServer restores and connects the saved
	// sessions, and each one reads the version as it connects.
	refreshWAVersion(ctx, log)

	srv, err := newServer(ctx, cfg, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	if cfg.token == "" && len(cfg.apiKeys) == 0 {
		log.Warn("no -token and no -api-key set: anyone who can reach this address can pair accounts and place calls")
	} else if cfg.token == "" {
		log.Warn("-api-key is set but -token is not: the browser client has no credential and will be rejected")
	}
	if *allowOrigin == "*" {
		log.Warn("-allow-origin '*' lets any website the operator visits drive this server; use it for development only")
	}
	if *recordAll && *recordDir == "" {
		log.Warn("-record-all has no effect without -record-dir; nothing will be recorded")
	}
	defer srv.sessions.disconnectAll()

	if err := srv.sessions.Restore(ctx); err != nil {
		log.Error("session restore failed", "err", err)
		os.Exit(1)
	}

	httpSrv := &http.Server{Addr: cfg.addr, Handler: srv.routes()}
	go func() {
		log.Info("HTTP server listening", "addr", cfg.addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server error", "err", err)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	srv.broker.closeWebhooks()
}

// envPrefix is prepended to a flag's name to find its environment variable:
// -record-dir reads WACALLS_RECORD_DIR.
const envPrefix = "WACALLS_"

// defaultDeviceName is what WhatsApp lists this server as under Linked devices.
// Left alone, whatsmeow registers as "whatsmeow" — the library's name rather
// than the product's.
const defaultDeviceName = "ORBSDR"

// deviceVersion goes out next to the name. It is whatsmeow's own default; the
// number is not shown in the Linked devices list.
var deviceVersion = [3]uint32{0, 1, 0}

// applyDeviceName sets how WhatsApp shows this server under Linked devices.
//
// It only affects **new pairings**: the name travels in the registration
// payload, never the login one (whatsmeow's store/clientpayload.go), so accounts
// that are already linked keep whatever name they registered under until they
// are unlinked and paired again.
func applyDeviceName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		// Letting this through would pair accounts under an empty name, which is
		// worse than the default and needs another re-pairing to undo.
		return fmt.Errorf("device name cannot be empty")
	}
	store.SetOSInfo(name, deviceVersion)
	return nil
}

// applyEnvDefaults lets every flag also be set through the environment, which is
// how the container is configured — a compose or Portainer stack lists settings
// under `environment:`, not by rewriting the command line.
//
// The command line wins: only flags the caller did not pass are considered, so
// nothing changes for anyone already using flags.
func applyEnvDefaults(fs *flag.FlagSet, lookup func(string) (string, bool)) error {
	passed := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { passed[f.Name] = true })

	var err error
	fs.VisitAll(func(f *flag.Flag) {
		if err != nil || passed[f.Name] {
			return
		}
		name := envPrefix + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		value, ok := lookup(name)
		if !ok || value == "" {
			return
		}
		// A repeatable flag takes a comma-separated list, since the environment
		// cannot express the same key twice.
		if _, repeatable := f.Value.(*repeatedFlag); repeatable {
			for _, part := range strings.Split(value, ",") {
				if part = strings.TrimSpace(part); part != "" {
					if setErr := f.Value.Set(part); setErr != nil {
						err = fmt.Errorf("%s=%q: %w", name, part, setErr)
						return
					}
				}
			}
			return
		}
		if setErr := f.Value.Set(value); setErr != nil {
			err = fmt.Errorf("%s=%q: %w", name, value, setErr)
		}
	})
	return err
}

// repeatedFlag collects a flag that may be given more than once.
type repeatedFlag []string

func (f *repeatedFlag) String() string     { return strings.Join(*f, ",") }
func (f *repeatedFlag) Set(v string) error { *f = append(*f, v); return nil }

// parseAPIKeys turns repeated -api-key "name:secret" values into a lookup.
func parseAPIKeys(values []string) (apiKeys, error) {
	if len(values) == 0 {
		return nil, nil
	}
	keys := apiKeys{}
	for _, v := range values {
		name, secret, ok := strings.Cut(v, ":")
		name, secret = strings.TrimSpace(name), strings.TrimSpace(secret)
		if !ok || name == "" || secret == "" {
			return nil, fmt.Errorf("expected \"name:secret\", got %q", v)
		}
		if _, dup := keys[name]; dup {
			return nil, fmt.Errorf("duplicate api key name %q", name)
		}
		keys[name] = secret
	}
	return keys, nil
}
