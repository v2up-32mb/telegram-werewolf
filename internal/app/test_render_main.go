//go:build ignore

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	renderer, err := i18n.NewRenderer("zh-CN")
	if err != nil {
		fmt.Printf("renderer error: %v\n", err)
		return
	}

	// Test 1: Render error.invalid_input with /status
	text1, err := renderer.Render("error.invalid_input", map[string]any{
		"Detail": "/status",
	})
	if err != nil {
		fmt.Printf("render error: %v\n", err)
		return
	}
	fmt.Printf("Rendered text 1: %q\n", text1)

	// Test 2: Render menu.main
	text2, err := renderer.Render("menu.main", nil)
	if err != nil {
		fmt.Printf("render error: %v\n", err)
		return
	}
	fmt.Printf("Rendered text 2: %q\n", text2)

	// Test 3: Render help.commands
	text3, err := renderer.Render("help.commands", nil)
	if err != nil {
		fmt.Printf("render error: %v\n", err)
		return
	}
	fmt.Printf("Rendered text 3: %q\n", text3)

	// Now send via go-telegram/bot
	b, err := bot.New("8900327091:AAEAefDWEAwwS7w7FJEMD5zUyJUoYU5WiYk")
	if err != nil {
		fmt.Printf("bot.New error: %v\n", err)
		return
	}

	// Send text1
	_, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    2027538584,
		Text:      text1,
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		fmt.Printf("Send 1 error: %v (type: %T)\n", err, err)
	} else {
		fmt.Println("Send 1: SUCCESS")
	}

	// Send text2
	_, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    2027538584,
		Text:      text2,
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		fmt.Printf("Send 2 error: %v (type: %T)\n", err, err)
	} else {
		fmt.Println("Send 2: SUCCESS")
	}

	// Send text3
	_, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    2027538584,
		Text:      text3,
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		fmt.Printf("Send 3 error: %v (type: %T)\n", err, err)
	} else {
		fmt.Println("Send 3: SUCCESS")
	}
}
