package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type Notifier interface {
	Send(ctx context.Context, msg string)
	Snipe(ctx context.Context, chain, symbol, token string, amount, price float64, shadow bool)
	Exit(ctx context.Context, chain, symbol string, pnlPct float64, reason string)
	DrawdownWarning(ctx context.Context, chain string, drawdownPct float64)
}

type WebhookNotifier struct {
	telegramToken string
	telegramChat  string
	discordURL    string
	client        *http.Client
	log           *slog.Logger
}

func New(telegramToken, telegramChat, discordURL string, log *slog.Logger) *WebhookNotifier {
	return &WebhookNotifier{
		telegramToken: telegramToken,
		telegramChat:  telegramChat,
		discordURL:    discordURL,
		client:        &http.Client{Timeout: 10 * time.Second},
		log:           log,
	}
}

func (n *WebhookNotifier) Send(ctx context.Context, msg string) {
	if n.telegramToken != "" && n.telegramChat != "" {
		go n.sendTelegram(ctx, msg)
	}
	if n.discordURL != "" {
		go n.sendDiscord(ctx, msg)
	}
}

func (n *WebhookNotifier) Snipe(ctx context.Context, chain, symbol, token string, amount, price float64, shadow bool) {
	mode := "LIVE"
	if shadow {
		mode = "SHADOW"
	}
	msg := fmt.Sprintf("[%s] SNIPE %s on %s\nToken: %s\nAmount: %.6f\nPrice: %.8f", mode, symbol, chain, token, amount, price)
	n.Send(ctx, msg)
}

func (n *WebhookNotifier) Exit(ctx context.Context, chain, symbol string, pnlPct float64, reason string) {
	msg := fmt.Sprintf("EXIT %s on %s\nPnL: %.1f%%\nReason: %s", symbol, chain, pnlPct, reason)
	n.Send(ctx, msg)
}

func (n *WebhookNotifier) DrawdownWarning(ctx context.Context, chain string, drawdownPct float64) {
	msg := fmt.Sprintf("DRAWDOWN WARNING: %s at %.1f%%", chain, drawdownPct)
	n.Send(ctx, msg)
}

func (n *WebhookNotifier) sendTelegram(ctx context.Context, msg string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.telegramToken)
	body, _ := json.Marshal(map[string]string{
		"chat_id": n.telegramChat,
		"text":    msg,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		n.log.Error("telegram request error", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		n.log.Error("telegram send error", "err", err)
		return
	}
	resp.Body.Close()
}

func (n *WebhookNotifier) sendDiscord(ctx context.Context, msg string) {
	body, _ := json.Marshal(map[string]string{
		"content": msg,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.discordURL, bytes.NewReader(body))
	if err != nil {
		n.log.Error("discord request error", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		n.log.Error("discord send error", "err", err)
		return
	}
	resp.Body.Close()
}
