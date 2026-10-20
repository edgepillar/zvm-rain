// Package tg is the few Bot API methods the rain bot needs, over plain HTTPS:
// long-polled updates in, HTML messages out. Copied from zvm-bullet/internal/tg,
// with the one getMe field that says whether the bot sees every group message.
package tg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const api = "https://api.telegram.org"

type Client struct {
	token string
	http  *http.Client
	base  string
	log   *slog.Logger
}

func New(token string, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	// The timeout covers a 30 s long poll with room to spare.
	return &Client{token: token, http: &http.Client{Timeout: 45 * time.Second}, base: api, log: log}
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`

	// CanReadAll is getMe's can_read_all_group_messages: false while
	// BotFather's privacy mode is on, when a group shows the bot only commands
	// and replies to it, and rain would see nobody talking.
	CanReadAll bool `json:"can_read_all_group_messages"`
}

// Name is how the bot addresses a person: their first name, or their
// username when Telegram has no first name.
func (u *User) Name() string {
	n := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if n == "" {
		n = u.Username
	}
	if n == "" {
		n = fmt.Sprintf("player %d", u.ID)
	}
	return n
}

type Chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"` // private | group | supergroup | channel
	Title string `json:"title"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
	// NewChatMembers is set on the service message for people joining; the
	// bot finds itself here when it is added to a group.
	NewChatMembers []User `json:"new_chat_members"`
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type apiError struct {
	Description string
	RetryAfter  int
}

func (e *apiError) Error() string { return "telegram: " + e.Description }

// call posts a method and unwraps Telegram's {ok, result, description}. A
// 429 is waited out and retried: Telegram names the wait in retry_after.
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/bot%s/%s", c.base, c.token, method), bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			// The URL carries the token; never let it reach a log.
			return fmt.Errorf("telegram %s: %s", method, strings.ReplaceAll(err.Error(), c.token, "<token>"))
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		var env struct {
			OK          bool            `json:"ok"`
			Result      json.RawMessage `json:"result"`
			Description string          `json:"description"`
			Parameters  struct {
				RetryAfter int `json:"retry_after"`
			} `json:"parameters"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return fmt.Errorf("telegram %s: %s", method, resp.Status)
		}
		if !env.OK {
			if resp.StatusCode == http.StatusTooManyRequests && attempt < 5 {
				wait := time.Duration(env.Parameters.RetryAfter+1) * time.Second
				c.log.Warn("telegram rate limit", "method", method, "wait", wait)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(wait):
				}
				continue
			}
			return &apiError{Description: env.Description, RetryAfter: env.Parameters.RetryAfter}
		}
		if out != nil {
			return json.Unmarshal(env.Result, out)
		}
		return nil
	}
}

func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var u User
	return &u, c.call(ctx, "getMe", struct{}{}, &u)
}

// Updates long-polls from offset.
func (c *Client) Updates(ctx context.Context, offset int64) ([]Update, error) {
	var out []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": 30, "allowed_updates": []string{"message"},
	}, &out)
	return out, err
}

// Send posts an HTML message. Link previews are off: every rain carries an
// explorer link, and a preview card under each would bury the chat.
func (c *Client) Send(ctx context.Context, chat int64, html string) error {
	return c.call(ctx, "sendMessage", map[string]any{
		"chat_id": chat, "text": html, "parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}, nil)
}

type Command struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetCommands registers the "/" menu for one scope ("default",
// "all_private_chats" or "all_group_chats").
func (c *Client) SetCommands(ctx context.Context, scope string, cmds []Command) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": cmds, "scope": map[string]string{"type": scope}}, nil)
}

// SetDescription sets the text on the bot's empty chat, before anyone has
// pressed Start (up to 512 characters), and the short one on its profile and
// in shared links (up to 120).
func (c *Client) SetDescription(ctx context.Context, long, short string) error {
	if err := c.call(ctx, "setMyDescription", map[string]any{"description": long}, nil); err != nil {
		return err
	}
	return c.call(ctx, "setMyShortDescription", map[string]any{"short_description": short}, nil)
}

// IsForbidden reports a message Telegram refused because the bot may not
// write there: a user who never opened a DM with it, or a group it left.
func IsForbidden(err error) bool {
	var e *apiError
	return errors.As(err, &e) && strings.Contains(strings.ToLower(e.Description), "forbidden")
}
