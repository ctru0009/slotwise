// Package email holds the outbound message senders. Development logs instead
// of delivering, which is what SPEC asks for until M5 wires a real provider.
package email

import (
	"context"
	"log/slog"

	"github.com/ctru0009/slotwise/internal/domain"
)

// LogSender writes messages to the log. The reset link is part of the logged
// body, so this is for development only.
type LogSender struct {
	logger *slog.Logger
}

// NewLogSender returns a sender logging through logger; a nil logger means
// slog.Default().
func NewLogSender(logger *slog.Logger) *LogSender {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogSender{logger: logger}
}

// Send logs the message and reports success; it never blocks on a network.
func (s *LogSender) Send(ctx context.Context, msg domain.Message) error {
	s.logger.InfoContext(ctx, "email", "to", msg.To, "subject", msg.Subject, "body", msg.Body)
	return nil
}
