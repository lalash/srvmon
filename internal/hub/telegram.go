package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var telegramClient = &http.Client{Timeout: 10 * time.Second}

// sendTelegram delivers one alert. It is a no-op when the operator has not
// configured a bot, so alert evaluation never depends on it.
func (h *Hub) sendTelegram(message string) error {
	token := strings.TrimSpace(h.store.Setting(keyTelegramToken, ""))
	chatID := strings.TrimSpace(h.store.Setting(keyTelegramChat, ""))
	if token == "" || chatID == "" {
		return errors.New("telegram bot token and chat id are not configured")
	}

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", url.PathEscape(token))
	form := url.Values{
		"chat_id":    {chatID},
		"text":       {message},
		"parse_mode": {"Markdown"},
	}

	resp, err := telegramClient.PostForm(endpoint, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram returned %s: %s", resp.Status, telegramReason(body))
	}
	return nil
}

func telegramReason(body []byte) string {
	var parsed struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Description != "" {
		return parsed.Description
	}
	return strings.TrimSpace(string(body))
}
