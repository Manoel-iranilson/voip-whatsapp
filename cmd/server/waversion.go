package main

import (
	"context"
	"log/slog"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
)

// knownWAVersion is a web client revision WhatsApp accepted more recently than
// the one compiled into our whatsmeow. It is the floor used when the live lookup
// below fails; bump it alongside whatsmeow.
var knownWAVersion = store.WAVersionContainer{2, 3000, 1050728964}

// refreshWAVersion makes new connections announce a web client version that
// WhatsApp still accepts. WhatsApp retires old revisions on its own schedule and
// answers them with 405 "client outdated", which stops pairing outright (no QR
// is ever issued) while already linked sessions often keep working. Looking the
// current revision up at startup keeps a deployed build pairing between
// whatsmeow upgrades. Must run before any client connects.
func refreshWAVersion(ctx context.Context, log *slog.Logger) {
	builtin := store.GetWAVersion()
	best := builtin
	if best.LessThan(knownWAVersion) {
		best = knownWAVersion
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if latest, err := whatsmeow.GetLatestVersion(ctx, nil); err != nil {
		log.Warn("could not fetch the current WhatsApp web version; using the newest known one",
			"err", err, "version", best.String())
	} else if best.LessThan(*latest) {
		best = *latest
	}

	if best != builtin {
		store.SetWAVersion(best)
	}
	log.Info("WhatsApp web version", "version", best.String(), "builtin", builtin.String())
}
