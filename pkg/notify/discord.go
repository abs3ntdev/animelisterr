// Package notify delivers operator notifications to Discord via an
// incoming webhook. Only rich-embed messages are supported, which gives
// us coloured severity, named fields for per-entry detail, and a
// timestamp — the same look-and-feel as Sonarr/Radarr's own Discord
// integration.
//
// The webhook URL comes from DISCORD_WEBHOOK_URL at startup. When the
// variable is unset, Client is a silent no-op so production operators
// can turn notifications off without touching the code.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Colour values Discord renders as the left border of an embed. Chosen
// to mirror the convention used by most Arr stack notifiers.
const (
	ColourSuccess = 0x2ECC71 // green: new week, list updated
	ColourWarning = 0xF1C40F // yellow: partial failure, match issues
	ColourError   = 0xE74C3C // red: refresh broke
)

// Embed is the subset of Discord's embed object we use. See
// https://discord.com/developers/docs/resources/channel#embed-object.
type Embed struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	Colour      int          `json:"color,omitempty"`
	Timestamp   string       `json:"timestamp,omitempty"` // ISO-8601
	Fields      []EmbedField `json:"fields,omitempty"`
	Footer      *EmbedFooter `json:"footer,omitempty"`
	URL         string       `json:"url,omitempty"`
}

// EmbedField is one name/value pair shown as a labelled row in the embed.
type EmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// EmbedFooter appears in small text at the bottom of the embed.
type EmbedFooter struct {
	Text string `json:"text"`
}

// payload is the full webhook body. Discord accepts either `content` (a
// plain string) or `embeds` (up to 10). We always use embeds.
type payload struct {
	Username  string  `json:"username,omitempty"`
	AvatarURL string  `json:"avatar_url,omitempty"`
	Embeds    []Embed `json:"embeds"`
}

// Client posts embeds to a Discord incoming webhook. The zero value is a
// silent no-op, which is convenient for tests and for deployments that
// don't configure DISCORD_WEBHOOK_URL. Errors from Discord (e.g. rate
// limits) are logged internally but never propagated — a broken webhook
// must not break the refresh loop.
type Client struct {
	URL       string
	Username  string
	HTTP      *http.Client
	UserAgent string

	// failureCooldown prevents notification spam when a long-running
	// outage fires off a failure per hourly tick. Zero disables the
	// cooldown.
	failureCooldown time.Duration
	mu              sync.Mutex
	lastFailureSent time.Time
}

// New returns a Client. When webhookURL is empty, every Send call is a
// no-op. Callers should always pass the zero-value Client around rather
// than nil — this keeps call sites free of nil checks.
func New(webhookURL string) *Client {
	return &Client{
		URL:             webhookURL,
		Username:        "animelisterr",
		UserAgent:       "animelisterr/1.0",
		HTTP:            &http.Client{Timeout: 10 * time.Second},
		failureCooldown: 4 * time.Hour,
	}
}

// Enabled reports whether this client will actually post. Callers can use
// this to skip expensive payload building when notifications are off.
func (c *Client) Enabled() bool { return c != nil && c.URL != "" }

// Send posts an embed to Discord. Any HTTP error is swallowed; the caller
// should not treat notification failure as a user-visible error.
func (c *Client) Send(ctx context.Context, e Embed) {
	if !c.Enabled() {
		return
	}
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	body, err := json.Marshal(payload{
		Username: c.Username,
		Embeds:   []Embed{e},
	})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
	// Discord returns 204 on success, 429 when rate-limited. We
	// intentionally ignore both — retrying would only amplify spam.
}

// SendFailure posts an error embed, but only if the failure-cooldown
// window has elapsed since the last failure notification. This prevents
// a persistent outage (feed unreachable for hours) from flooding the
// channel with a message every refresh tick.
func (c *Client) SendFailure(ctx context.Context, title, detail string) {
	if !c.Enabled() {
		return
	}
	c.mu.Lock()
	if c.failureCooldown > 0 && time.Since(c.lastFailureSent) < c.failureCooldown {
		c.mu.Unlock()
		return
	}
	c.lastFailureSent = time.Now()
	c.mu.Unlock()

	c.Send(ctx, Embed{
		Title:       title,
		Description: truncate(detail, 1800), // Discord's embed desc cap is 4096; leave headroom
		Colour:      ColourError,
		Footer:      &EmbedFooter{Text: fmt.Sprintf("further failures in the next %s will be suppressed", c.failureCooldown)},
	})
}

// truncate clips s to max runes, appending an ellipsis when cut. Used to
// keep Discord payloads well under their documented limits.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
