# Telegram 狼人杀 Bot

纯私聊模式的 Telegram 狼人杀游戏（6 人局 MVP）。

- **当前状态**：MVP 已实现——房间、发牌、完整夜间、白天发言、投票、结算、
  积分、再来一局与容灾中止；配套 GitHub Actions 质量门禁与
  Release/GHCR 多架构镜像发布流水线
- **核心模式**：方案 B —— 纯私聊逻辑房间（不建群、不拉 Bot、不授权）

## 功能

- 6 人局：2 狼 + 预言家 + 女巫 + 2 平民（默认屠城，可配置）
- 创建/加入房间：房间码（随机/自定义）、密码、自定义昵称
- 发牌仪式与狼人标识（🐺 + 队友名单）
- 完整夜间：狼人刀人、女巫救/毒（首夜自救可配）、预言家查验
- 白天发言与匿名投票，含复杂平票与最终二人对决
- 结算、战报、积分（胜 5 / 败 0 / 死亡且阵营胜 2；恶意退出按规则不得分或扣 5）、再来一局
- 容灾：进程重启中止局不结算并保留中止记录；恶意退出 10 分钟冷却
- 发布：tag 时由 GitHub Actions 生成 linux/amd64、linux/arm64 二进制与
  校验和，并用 Buildx 构建多架构镜像推送 GHCR

## 快速开始

前置：Go 1.25.x（`go.mod` 声明 `go 1.25.13`）；本机需能访问 GitHub/Go 模块源。

```bash
export PATH=/usr/local/go/bin:$PATH

# 1) 配置（敏感值不进 YAML，只走环境变量）
cp config.example.yaml config.yaml
export TELEGRAM_BOT_TOKEN=123456:your-bot-token

# 2) 本地门禁（vet + test + build + lint + gofmt + diff）
make check

# 3) 构建并启动（Long Polling）
make build
bin/werewolf serve --config config.yaml
```

## 测试与门禁

| 命令 | 说明 |
|---|---|
| `go test ./...` | 全量测试（含 Fake Telegram Bot API 的 E2E，无需真实 Token） |
| `make check` | 本地全量门禁（vet/test/build/lint/gofmt/diff） |
| `make build-all` | 交叉编译 linux/amd64、linux/arm64 |
| `go tool govulncheck ./...` | 依赖漏洞扫描（需网络） |
| GitHub Actions | push main / PR 自动跑全套 CI；`v*` tag 自动发布 Release + GHCR |

新开发者从零到可运行/可测试的细节见 [docs/开发指南.md](docs/开发指南.md)。

## 文档

| 文档 | 用途 |
|---|---|
| [docs/方案设计.md](docs/方案设计.md) | 玩法总览（定位、角色、状态机、里程碑） |
| [docs/游戏流程设计.md](docs/游戏流程设计.md) | **唯一权威细节来源**（创建/加入/进行/结算 + 补充规则） |
| [docs/设计Q&A.md](docs/设计Q&A.md) | 关键决策的 Q&A 记录 |
| [docs/角色卡片.md](docs/角色卡片.md) | 角色文字描述与静态资源目录约定 |
| [docs/阶段消息设计.md](docs/阶段消息设计.md) | **阶段消息展示权威来源**（主消息、临时操作、私密视图、上帝视角） |
| [docs/阶段消息Q&A.md](docs/阶段消息Q&A.md) | 阶段消息 Q1～Q56 决策记录 |
| [docs/技术选型.md](docs/技术选型.md) | 技术架构与选型权威来源（Go 1.25、SQLite、CI/CD、GHCR） |
| [docs/开发指南.md](docs/开发指南.md) | 新开发者从零运行、测试、生成与提交约定 |
| [docs/部署与运维.md](docs/部署与运维.md) | systemd/容器部署、健康检查、备份恢复、重启中止语义 |
| [docs/测试验收清单.md](docs/测试验收清单.md) | MVP 规则逐条验收映射 |

## 仓库结构

```text
telegram-werewolf/
├── cmd/werewolf/            # 入口（serve / backup）
├── internal/                # 业务实现（app/game/room/storage/telegram/outbox/...）
├── migrations/              # SQLite 纯 SQL 迁移（goose，嵌入二进制）
├── queries/                 # sqlc 查询定义
├── assets/                  # 静态资源（角色卡片等）
├── deploy/                  # systemd 单元与部署配置示例
├── .github/workflows/       # CI 与 Release 流水线
├── testdata/scenarios/      # E2E 场景（好人胜/狼人胜/平票/重启中止）
├── docs/                    # 权威设计与本组手册
├── Makefile                 # 本地门禁目标
├── config.example.yaml      # 配置示例（复制为 config.yaml）
└── sqlc.yaml                # sqlc 配置
```
