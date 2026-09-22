package main

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/pion/webrtc/v4"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"
)

// config is everything main() gathers from flags. It is passed whole so adding a
// knob does not keep widening newServer's signature.
type config struct {
	addr        string
	dbPath      string
	staticDir   string
	allowOrigin string
	token       string
	maxCalls    int
	recordDir   string
	recordAll   bool

	apiKeys       apiKeys
	webhookURL    string
	webhookSecret string
	ice           iceConfig
}

type server struct {
	broker    *Broker
	sessions  *SessionManager
	log       *slog.Logger
	staticDir string
	origins   originPolicy
	token     string
	// store and recordDir back the recording download route, which has to find a
	// file for a call that has already ended.
	store     *sessionStore
	recordDir string
	recordAll bool
	apiKeys   apiKeys
	// browserAPI carries the ICE settings that make this server reachable from a
	// browser; every Bridge is built from it.
	browserAPI *webrtc.API
}

func openDB(dbPath string) (*sql.DB, error) {
	dsn := "file:" + dbPath + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func newServer(ctx context.Context, cfg config, log *slog.Logger) (*server, error) {
	// Fail here rather than on the first call: a bad range would otherwise only
	// surface when an operator tries to talk.
	browserAPI, err := newBrowserAPI(cfg.ice)
	if err != nil {
		return nil, err
	}
	db, err := openDB(cfg.dbPath)
	if err != nil {
		return nil, err
	}
	container := sqlstore.NewWithDB(db, "sqlite3", waLog.Noop)
	if err := container.Upgrade(ctx); err != nil {
		return nil, err
	}
	store, err := newSessionStore(ctx, db)
	if err != nil {
		return nil, err
	}

	waLogger := waLog.Noop
	if log.Enabled(ctx, slog.LevelDebug) {
		waLogger = waLog.Stdout("WA", "INFO", true)
	}

	broker := NewBroker()
	broker.history = store
	if cfg.webhookURL != "" {
		broker.webhooks = newWebhookSender(cfg.webhookURL, cfg.webhookSecret, log)
		log.Info("webhooks enabled", "url", cfg.webhookURL, "signed", cfg.webhookSecret != "")
	}
	mgr := newSessionManager(ctx, container, broker, store, waLogger, log, cfg)
	broker.SnapshotFn = mgr.snapshotEvents

	return &server{
		broker:     broker,
		sessions:   mgr,
		log:        log,
		staticDir:  cfg.staticDir,
		origins:    newOriginPolicy(cfg.allowOrigin),
		token:      cfg.token,
		store:      store,
		recordDir:  cfg.recordDir,
		recordAll:  cfg.recordAll,
		apiKeys:    cfg.apiKeys,
		browserAPI: browserAPI,
	}, nil
}
