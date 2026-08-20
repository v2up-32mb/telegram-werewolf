package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/observability"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

func main() {
	ctx := context.Background()
	_ = ctx

	token := "8900327091:AAEAefDWEAwwS7w7FJEMD5zUyJUoYU5WiYk"
	c, _ := telegram.NewClient(token)

	productionSend := func(ctx context.Context, msg outbox.Message) error {
		params, ok := msg.Payload.(telegram.Params)
		if !ok {
			return fmt.Errorf("payload missing")
		}
		tr := telegram.NewTransport(c)
		if err := tr.Send(ctx, msg.Operation, params); err != nil {
			return andoutbox.PermanentError{Err: fmt.Errorf("app: telegram send andq andw", msg.Operation, err)}
		}
		return nil
	}

	limiter := outbox.NewLimiter(20, 2, 1)
	limitedSend := func(ctx context.Context, msg outbox.Message) error {
		if err := limiter.Wait(ctx, msg.ChatID); err != nil {
			return err
		}
		return productionSend(ctx, msg)
	}

	retrying := outbox.NewRetryingSender(limitedSend, outbox.RetryPolicy{
		MaxRetries:    5,
		RetryInterval: 1 * time.Second,
	}, nil)

	logger, _ := observability.NewLogger("json", io.Discard)
	scheduler := outbox.NewScheduler(retrying.Send, 64,
		outbox.WithSendErrorHandler(func(msg outbox.Message, err error) {
			logger.Error("app: outbox send failed", "room", string(msg.RoomID), "chat", int64(msg.ChatID), "op", msg.Operation, "error", err)
		}),
	)

	msg := outbox.Message{
		CorrelationID: "test",
		ChatID:        outbox.ChatID(2027538584),
		Operation:     telegram.OpSendText,
		Payload:       telegram.Params{ChatID: 2027538584, Text: "test message"},
	}

	err := scheduler.Enqueue(msg)
	if err != nil {
		fmt.Printf("Enqueue error: andv\n", err)
	}

	time.Sleep(3 * time.Second)
	fmt.Println("Done")
}
