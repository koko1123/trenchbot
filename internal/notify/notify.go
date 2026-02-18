package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/getsentry/sentry-go"
)

type Notifier interface {
	Send(ctx context.Context, msg string)
	Snipe(ctx context.Context, chain, symbol, token string, amount, price float64, shadow bool)
	Exit(ctx context.Context, chain, symbol, tokenAddress string, pnlPct float64, reason string)
	DrawdownWarning(ctx context.Context, chain string, drawdownPct float64)
}

// Init initializes the Sentry SDK. If dsn is empty, Sentry operates in no-op mode.
func Init(dsn, environment string) error {
	if dsn == "" {
		return nil
	}
	return sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      environment,
		SampleRate:       1.0,
		TracesSampleRate: 0,
	})
}

// Flush drains the Sentry event queue.
func Flush(timeout time.Duration) {
	sentry.Flush(timeout)
}

type SentryNotifier struct {
	enabled bool
	log     *slog.Logger
}

func New(dsn string, log *slog.Logger) *SentryNotifier {
	return &SentryNotifier{
		enabled: dsn != "",
		log:     log,
	}
}

func (n *SentryNotifier) Send(_ context.Context, msg string) {
	if !n.enabled {
		return
	}
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelInfo)
		scope.SetTag("type", "summary")
		sentry.CaptureMessage(msg)
	})
}

func (n *SentryNotifier) Snipe(_ context.Context, chain, symbol, token string, amount, price float64, shadow bool) {
	mode := "live"
	if shadow {
		mode = "shadow"
	}
	n.log.Info("SNIPE", "chain", chain, "symbol", symbol, "token", token, "amount", amount, "price", price, "mode", mode)

	if !n.enabled {
		return
	}
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelInfo)
		scope.SetTag("chain", chain)
		scope.SetTag("symbol", symbol)
		scope.SetTag("mode", mode)
		scope.SetExtra("token", token)
		scope.SetExtra("amount", amount)
		scope.SetExtra("price", price)
		sentry.CaptureMessage(fmt.Sprintf("SNIPE %s on %s (%.6f @ %.8f)", symbol, chain, amount, price))
	})
}

func (n *SentryNotifier) Exit(_ context.Context, chain, symbol, tokenAddress string, pnlPct float64, reason string) {
	n.log.Info("EXIT", "chain", chain, "symbol", symbol, "token", tokenAddress, "pnl_pct", pnlPct, "reason", reason)

	if !n.enabled {
		return
	}
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelInfo)
		scope.SetTag("chain", chain)
		scope.SetTag("symbol", symbol)
		scope.SetTag("reason", reason)
		scope.SetExtra("pnl_pct", pnlPct)
		sentry.CaptureMessage(fmt.Sprintf("EXIT %s on %s: %.1f%% (%s)", symbol, chain, pnlPct, reason))
	})
}

func (n *SentryNotifier) DrawdownWarning(_ context.Context, chain string, drawdownPct float64) {
	n.log.Warn("DRAWDOWN", "chain", chain, "drawdown_pct", drawdownPct)

	if !n.enabled {
		return
	}
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelWarning)
		scope.SetTag("chain", chain)
		scope.SetExtra("drawdown_pct", drawdownPct)
		sentry.CaptureMessage(fmt.Sprintf("DRAWDOWN WARNING: %s at %.1f%%", chain, drawdownPct))
	})
}
