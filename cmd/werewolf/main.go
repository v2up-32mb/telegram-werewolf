package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/v2up-32mb/telegram-werewolf/internal/app"
	"github.com/v2up-32mb/telegram-werewolf/internal/config"
	"github.com/v2up-32mb/telegram-werewolf/internal/observability"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return nil
	}
	switch args[1] {
	case "backup":
		return runBackup(ctx, args[2:], stdout)
	case "serve":
		return runServe(ctx, args[2:], stderr)
	default:
		return fmt.Errorf("未知子命令 %q", args[1])
	}
}

// runServe 装配应用并以 Long Polling 方式启动，阻塞至 ctx 取消后优雅退出。
//
// 用法：werewolf serve [--config <path>]；Bot Token 由环境变量
// TELEGRAM_BOT_TOKEN 提供（config.Load 校验）。
func runServe(ctx context.Context, args []string, stderr io.Writer) error {
	var configPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			i++
			if i >= len(args) {
				return errors.New("werewolf serve: --config 缺少参数")
			}
			configPath = args[i]
		default:
			return fmt.Errorf("werewolf serve: 未知参数 %q", args[i])
		}
	}
	if configPath == "" {
		configPath = "config.yaml"
	}

	cfg, err := config.Load(configPath, os.LookupEnv)
	if err != nil {
		return err
	}
	logger, err := observability.NewLogger(cfg.LogFormat, stderr)
	if err != nil {
		return err
	}
	wiring, err := app.NewWiring(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("werewolf serve: wiring: %w", err)
	}

	// Build 可能因 Telegram getMe 网络握手等待较久；放在 goroutine 中，
	// 启动阶段收到 SIGTERM/SIGINT 时直接优雅返回，不阻塞停机。
	type buildResult struct {
		instance *app.App
		err      error
	}
	buildCh := make(chan buildResult, 1)
	go func() {
		instance, err := app.Build(ctx, cfg, app.WithLogger(logger), app.WithWiring(wiring))
		buildCh <- buildResult{instance: instance, err: err}
	}()
	select {
	case res := <-buildCh:
		if res.err != nil {
			return res.err
		}
		return res.instance.Run(ctx)
	case <-ctx.Done():
		return nil
	}
}

// runBackup 执行 werewolf backup --output <path> [--config <path>]：
// 从配置读取 database_path，使用 SQLite 一致性快照生成备份。
func runBackup(ctx context.Context, args []string, stdout io.Writer) error {
	var configPath, output string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			i++
			if i >= len(args) {
				return errors.New("werewolf backup: --config 缺少参数")
			}
			configPath = args[i]
		case "--output":
			i++
			if i >= len(args) {
				return errors.New("werewolf backup: --output 缺少参数")
			}
			output = args[i]
		default:
			return fmt.Errorf("werewolf backup: 未知参数 %q", args[i])
		}
	}
	if output == "" {
		return errors.New("werewolf backup: 缺少 --output <path>")
	}
	if configPath == "" {
		configPath = "config.yaml"
	}

	cfg, err := config.Load(configPath, os.LookupEnv)
	if err != nil {
		return err
	}
	db, err := storage.Open(cfg.DatabasePath, storage.DefaultMaxOpenConns)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := storage.Backup(ctx, db, output); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "backup written to %s (integrity ok)\n", output)
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
