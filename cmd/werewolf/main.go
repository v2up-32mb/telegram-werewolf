package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/v2up-32mb/telegram-werewolf/internal/config"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return nil
	}
	switch args[1] {
	case "backup":
		return runBackup(ctx, args[2:], stdout)
	default:
		return fmt.Errorf("未知子命令 %q", args[1])
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
	if err := run(context.Background(), os.Args, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
