# Telegram 狼人杀 Bot MVP 实施计划

> **For Hermes:** Use subagent-driven-development skill to implement this plan task-by-task.

**Goal:** 在已终审的玩法与技术选型基础上，交付一个可在 Telegram 纯私聊中完成 6 人局全流程的 Go MVP，并通过自动化测试、Race Detector、SQLite migration、跨架构构建和真实 Bot 冒烟验证。

**Architecture:** 采用单进程模块化单体。每个活跃房间由一个 goroutine 独占状态，所有输入经 Command channel 串行进入强类型 Reducer；Reducer 只返回新状态和 Effects，Telegram、Outbox、Timer、SQLite 等副作用由外层 Adapter 执行。进行中状态不跨重启恢复，SQLite 只保存长期数据和重启中止所需的最小活跃记录。

**Tech Stack:** Go 1.25、`github.com/go-telegram/bot`、Long Polling、`modernc.org/sqlite`、`database/sql`、`sqlc`、`pressly/goose/v3`、`go-i18n/v2`、Telegram MarkdownV2、`log/slog`、GitHub Actions、GitHub Releases、GHCR。

---

## 0. 权威来源与范围

实施前必须阅读：

- `docs/游戏流程设计.md`：玩法细节唯一权威来源；
- `docs/方案设计.md`：P0/P1/P2 与 6 人局范围；
- `docs/技术选型.md`：技术架构唯一权威来源；
- `docs/技术选型Q&A.md`：选型理由和明确排除项；
- `docs/角色卡片.md`：角色卡文案与文件名；
- `docs/阶段消息设计.md`：阶段主消息、临时操作、按钮、私密标记和上帝视角展示唯一权威来源；
- `docs/阶段消息Q&A.md`：阶段消息 Q1～Q56 决策记录。

### MVP 包含

- P0：建房、房间码/邀请链接/二维码、密码、加入、昵称、房间面板、满员开局；
- P1：6 人发牌确认、2 狼、预言家、女巫、2 平民、完整夜间流程；
- P2：白天播报、麦序发言、匿名投票、平票流程、狼人自爆、胜负与结算；
- 系统闭环：退出/超时、房主移交、投票踢人/解散、积分、战报、中止清场、再来一局；
- 工程闭环：SQLite、Outbox、i18n、MarkdownV2、日志、健康检查、备份、CI、Release。

### MVP 不包含

- AI 实现（只保留 `PlayerController` 接口）；
- 猎人、守卫、8/9/12 人局；
- 观战入口、排行榜、完整回放；
- Web 管理后台、Webhook 生产实现；
- Redis/RabbitMQ、PostgreSQL、微服务、Event Sourcing；
- 对局跨进程重启恢复。

### 外部实现库补充

- Telegram Adapter 使用已终审的 `go-telegram/bot`；选型时 `v1.23.0` 已支持 Bot API 10.2。[1][2]
- 邀请二维码使用纯 Go 的 `github.com/yeqown/go-qrcode/v2`，初始化工程时锁定已验证的 `v2.2.5`。[3][4]
- SQL migration 使用 `github.com/pressly/goose/v3` 的 library mode 和 embedded SQL migrations，初始化工程时锁定已验证的 `v3.27.3`；不编写 Go migration。[5][6][7]

---

## 1. 全程执行规则

1. 每个任务先写失败测试，再写最小实现，再重构。
2. 每个任务结束必须运行该任务的定向测试；每个阶段结束运行全量测试。
3. 每个任务一个小提交；不得把多个阶段混成“大爆炸提交”。
4. 只实现当前任务需要的接口；禁止为后期猎人、守卫、AI、观战提前写空壳。
5. 所有时间与随机行为必须可注入；测试不得真实等待阶段秒数。
6. 核心 `internal/game` 禁止导入 Telegram、SQLite、日志、配置或模型 SDK。
7. 所有 Telegram 动态文本必须经过统一 MarkdownV2 escaper。
8. 所有需要编辑的消息由 Outbox 返回 correlation result，结果再作为 Command 回房间 Actor。
9. 正式构建固定 `CGO_ENABLED=0`；Race Detector 在独立原生 CI job 中允许启用 CGO。
10. 每个阶段使用独立分支和 PR：`feat/m0-foundation`、`feat/p0-room`、`feat/p1-night`、`feat/p2-game`、`chore/release`。

---

## 2. 目标目录结构

```text
telegram-werewolf/
├── cmd/werewolf/
│   └── main.go
├── internal/
│   ├── app/
│   ├── config/
│   ├── game/
│   ├── i18n/
│   ├── observability/
│   ├── outbox/
│   ├── player/
│   ├── room/
│   ├── storage/
│   │   └── sqlc/
│   └── telegram/
├── assets/
│   ├── embed.go
│   └── role-cards/
├── migrations/
│   ├── embed.go
│   └── 000001_initial.sql
├── queries/
├── .github/workflows/
├── config.example.yaml
├── sqlc.yaml
├── Makefile
├── go.mod
└── go.sum
```

`assets/embed.go` 必须位于 `assets/` 目录，因为 `go:embed` 不允许通过 `..` 嵌入仓库根目录下的资源。`migrations/embed.go` 同理放在 migration 目录内。

---

# 阶段 M0：工程骨架与可测试边界

## Task 1：初始化 Go module、工具锁定与基础命令

**Objective:** 建立可编译、可测试、可交叉构建的 Go 1.25 工程基线。

**Files:**

- Create: `go.mod`
- Create: `go.sum`
- Create: `Makefile`
- Create: `.golangci.yml`
- Modify: `.gitignore`
- Create: `cmd/werewolf/main.go`
- Test: `cmd/werewolf/main_test.go`

**Steps:**

1. 在 `cmd/werewolf/main_test.go` 写一个启动参数 smoke test，先验证包或入口尚不存在而失败。
2. 运行：`go test ./cmd/werewolf`；预期 FAIL。
3. 执行：`go mod init github.com/v2up-32mb/telegram-werewolf`，设置 `go 1.25.0`。
4. 添加最小 `main.go`，只解析 context 并以可测试的 `run(ctx, args, stdout, stderr) error` 入口返回。
5. 在 `.gitignore` 增加 `.hermes/`、`/bin/`、`/data/`、`*.db`、`*.db-wal`、`*.db-shm`、`coverage.out`。
6. Makefile 提供 `fmt`、`vet`、`test`、`test-race`、`build`、`build-all`、`generate`、`check` 目标。
7. 运行：`gofmt -w cmd/werewolf/*.go && go test ./cmd/werewolf && CGO_ENABLED=0 go build ./cmd/werewolf`；预期 PASS。
8. Commit：`chore: initialize Go project skeleton`。

## Task 2：锁定运行时与开发依赖

**Objective:** 将终审依赖写入 `go.mod/go.sum`，CI 不使用浮动版本。

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/app/dependencies_test.go`

**Steps:**

1. 写测试/检查脚本，断言 module path、Go version 和关键 module 均已锁定；先运行并观察 FAIL。
2. 锁定 `github.com/go-telegram/bot@v1.23.0`、`github.com/yeqown/go-qrcode/v2@v2.2.5`、`github.com/pressly/goose/v3@v3.27.3`。
3. 加入 `modernc.org/sqlite`、`github.com/nicksnyder/go-i18n/v2`、`go.yaml.in/yaml/v3`、`golang.org/x/time/rate`、`golang.org/x/crypto` 和 `github.com/google/go-cmp`，由 `go get` 将执行时解析到的精确版本写入 `go.mod/go.sum`。
4. 用 Go tool directive 锁定 `sqlc` 与 `govulncheck`；若 `golangci-lint` 不适合 tool directive，则在 CI action 中固定版本。
5. 运行：`go mod tidy && go mod verify && go test ./internal/app -run TestLockedDependencies`；预期 PASS。
6. Commit：`chore: lock project dependencies`。

## Task 3：实现配置加载与启动校验

**Objective:** 支持 YAML、环境变量密钥覆盖和明确的启动错误。

**Files:**

- Create: `config.example.yaml`
- Create: `internal/config/config.go`
- Create: `internal/config/load.go`
- Test: `internal/config/config_test.go`
- Test: `internal/config/load_test.go`

**Steps:**

1. 表格测试覆盖：默认值、YAML、环境变量覆盖、缺 Token、非法时长、Webhook 配置仅预留但 MVP 禁止启用。
2. 运行：`go test ./internal/config`；预期 FAIL。
3. 定义 `Config`：Bot token、database path、update mode、health address、log format、default locale、Outbox 参数。
4. 实现 `Load(path, lookupEnv)` 与 `Validate()`；错误必须指出具体字段。
5. `config.example.yaml` 不含真实 Token，Token 只通过 `TELEGRAM_BOT_TOKEN`。
6. 运行：`go test ./internal/config`；预期 PASS。
7. Commit：`feat: add validated application configuration`。

## Task 4：实现本地化与 MarkdownV2 安全渲染

**Objective:** 建立所有用户可见文本的唯一渲染入口。

**Files:**

- Create: `internal/i18n/localizer.go`
- Create: `internal/i18n/embed.go`
- Create: `internal/i18n/render.go`
- Create: `internal/i18n/markdown.go`
- Create: `internal/i18n/locales/active.zh-CN.yaml`
- Test: `internal/i18n/markdown_test.go`
- Test: `internal/i18n/render_test.go`
- Test: `internal/i18n/markdown_fuzz_test.go`

**Steps:**

1. 测试所有 MarkdownV2 特殊字符、中文昵称、下划线、括号、URL、反引号、玩家自由文本、身份卡 Caption、时间段主消息、临时操作消息和上帝视角文本。
2. 增加 Fuzz：任意 UTF-8 输入经 escape 后不得产生未转义控制字符或 panic。
3. 运行：`go test ./internal/i18n`；预期 FAIL。
4. 在 `internal/i18n/embed.go` 用 `//go:embed locales/*.yaml` 嵌入语言资源并注册 YAML unmarshal；实现 `EscapeMarkdownV2(string) string` 与 `Renderer.Render(messageKey, data)`；模板参数默认全部转义，只有显式 `SafeMarkdown` 可绕过。
5. 加入首批系统错误、主菜单、阶段消息、角色操作、顶部通知和健康提示文案 key；区分普通文字中的“Emoji + 明文”私密标记与按钮中“座位号 + Emoji”短标记。
6. 运行：`go test ./internal/i18n`；预期 PASS。
7. Commit：`feat: add i18n and MarkdownV2 renderer`。

## Task 5：实现结构化日志、计数器与健康检查

**Objective:** 提供不泄漏身份/密钥的最小可观测性。

**Files:**

- Create: `internal/observability/logging.go`
- Create: `internal/observability/metrics.go`
- Create: `internal/observability/health.go`
- Test: `internal/observability/logging_test.go`
- Test: `internal/observability/health_test.go`

**Steps:**

1. 测试文本/JSON handler、字段注入、Token 脱敏、`/healthz` 与 `/readyz` 状态码。
2. 运行：`go test ./internal/observability`；预期 FAIL。
3. 使用 `log/slog` 实现 logger factory；定义线程安全轻量 counters/gauges。
4. readiness 依赖可注入 checker，不直接引用 Telegram/SQLite 实现。
5. 运行：`go test ./internal/observability`；预期 PASS。
6. Commit：`feat: add logging and health endpoints`。

---

# 阶段 M1：纯领域模型、Reducer 与房间 Actor

## Task 6：定义领域 ID、枚举、配置与不变量

**Objective:** 用强类型固定 MVP 的角色、阶段和配置边界。

**Files:**

- Create: `internal/game/id.go`
- Create: `internal/game/role.go`
- Create: `internal/game/phase.go`
- Create: `internal/game/config.go`
- Create: `internal/game/player.go`
- Test: `internal/game/config_test.go`
- Test: `internal/game/invariants_test.go`

**Steps:**

1. 写测试：6 人牌组必须为 2 狼、预言家、女巫、2 平民；有效座位严格为 1～6、0 和 7 均非法；房主座位为 1；MVP 拒绝其他人数与 AI。
2. 运行：`go test ./internal/game -run 'TestConfig|TestInvariants'`；预期 FAIL。
3. 定义 `RoomID`、`GameID`、`UserID`、`Seat`、`Role`、`Camp`、`Phase`、`VictoryMode` 和 `GameConfig`。
4. 实现 `GameConfig.Validate()` 与状态不变量检查器。
5. 运行定向测试；预期 PASS。
6. Commit：`feat: define game domain types`。

## Task 7：定义 State、Command、Effect 与 PlayerController

**Objective:** 固定纯游戏核心的输入输出协议。

**Files:**

- Create: `internal/game/state.go`
- Create: `internal/game/command.go`
- Create: `internal/game/effect.go`
- Create: `internal/player/controller.go`
- Test: `internal/game/state_test.go`

**Key interface sketch:**

```go
type Reducer interface {
    Reduce(State, Command) (State, []Effect, error)
}

type CommandMeta struct {
    ID           string
    Actor        UserID
    ExpectedPhase Phase
    PhaseVersion uint64
    ReceivedAt   time.Time
}
```

**Steps:**

1. 测试 State 深复制/值语义、Command meta、Effect 分类和敏感视图不得混入公共 Effect。
2. 运行：`go test ./internal/game -run TestState`；预期 FAIL。
3. 实现 Lobby/Deal/Night/Day/Vote/Settled 所需的最小字段，避免猎人/守卫字段。
4. `PlayerController` 只暴露“给定可见视图，产生 Command”的边界，不实现 AI。
5. 运行定向测试；预期 PASS。
6. Commit：`feat: define reducer contracts`。

## Task 8：实现可注入随机源与 6 人牌组

**Objective:** 保证生产安全随机和测试确定性。

**Files:**

- Create: `internal/game/rng.go`
- Create: `internal/game/deck.go`
- Test: `internal/game/deck_test.go`
- Test: `internal/game/deck_fuzz_test.go`

**Steps:**

1. 写失败测试：角色数量、每人一张、同 seed fixture 可复现、Fisher–Yates 无越界。
2. 运行：`go test ./internal/game -run TestDeck`；预期 FAIL。
3. 定义窄 `RNG` 接口，生产实现基于 `crypto/rand` 的无偏整数采样。
4. 实现 Fisher–Yates；禁止 `math/rand` 进入生产路径。
5. 运行单测与 Fuzz smoke：`go test ./internal/game && go test ./internal/game -fuzz=FuzzDeck -fuzztime=5s`。
6. Commit：`feat: add cryptographic role shuffle`。

## Task 9：建立 Reducer 骨架与通用拒绝规则

**Objective:** 在实现角色规则前先统一阶段、权限与版本校验。

**Files:**

- Create: `internal/game/reducer.go`
- Create: `internal/game/errors.go`
- Test: `internal/game/reducer_test.go`
- Test: `internal/game/reducer_fuzz_test.go`

**Steps:**

1. 表格测试：错误阶段、错误 phaseVersion、非房间玩家、死亡玩家、重复 Command、非法目标。
2. 运行：`go test ./internal/game -run TestReducerRejects`；预期 FAIL。
3. 实现前置 validator，再分派到阶段 reducer；错误不得部分修改 State。
4. Fuzz 任意 Command 序列：不得 panic，状态必须持续满足不变量。
5. 运行：`go test ./internal/game`；预期 PASS。
6. Commit：`feat: add reducer validation shell`。

## Task 10：实现 Clock、Room Actor 与阶段版本计时

**Objective:** 串行执行同房间 Command，并安全拒绝旧 Timer。

**Files:**

- Create: `internal/room/clock.go`
- Create: `internal/room/actor.go`
- Create: `internal/room/result.go`
- Test: `internal/room/actor_test.go`
- Test: `internal/room/timer_test.go`

**Key actor sketch:**

```go
type Envelope struct {
    Command game.Command
    Reply   chan<- Result
}

type Actor struct {
    inbox   chan Envelope
    state   game.State
    reducer game.Reducer
    clock   Clock
}
```

**Steps:**

1. Fake Clock 测试同房间串行、Timer 触发、取消竞争、旧 `phaseVersion` 被拒绝。
2. 增加 deadline 边界测试：Telegram Command 在服务端标记的 `ReceivedAt <= phaseDeadline` 时，即使与 Timer 同时 ready 也必须先于 Timeout 生效；`ReceivedAt > phaseDeadline` 必须拒绝。Actor 在应用 Timeout 前先排空当前已缓冲 inbox，并按接收序处理 deadline 前事件。
3. 增加背压与 Timer 竞态测试：bounded inbox 满时 Dispatch 必须返回可观察的 `ErrInboxFull`/繁忙结果并增加计数器，禁止静默丢命令；覆盖 Timer 已触发后 `Stop()` 返回 false 的路径和 drain 行为。
4. 运行：`go test ./internal/room`；预期 FAIL。
5. 实现单 goroutine loop；Reducer 返回 Effects 后交给可注入 Effect sink。
6. Actor 停止必须取消 timer、关闭接收并等待 goroutine 退出。
7. 运行：`go test -race ./internal/room`；预期 PASS。
8. Commit：`feat: add room actor and versioned timers`。

## Task 11：实现 Room Manager 与生命周期注册表

**Objective:** 管理多房间并发、唯一 room/user 约束和干净退出。

**Files:**

- Create: `internal/room/manager.go`
- Create: `internal/room/code.go`
- Test: `internal/room/manager_test.go`
- Test: `internal/room/code_test.go`

**Steps:**

1. 测试：一个用户只能在一个进行中房间；房主只能创建一个；房间码去混淆字符；并发创建不重复。
2. 运行：`go test -race ./internal/room -run TestManager`；预期 FAIL。
3. 实现 Manager map 的最小同步范围；房间状态仍归 Actor 独占。
4. 房间码生成器注入 RNG，最终唯一性仍交由 storage unique constraint 确认并重试。
5. 运行定向测试和 race；预期 PASS。
6. Commit：`feat: add room manager lifecycle`。

---

# 阶段 M2：SQLite、sqlc 与中止清场

## Task 12：定义初始 schema 与 embedded migrations

**Objective:** 建立用户、活跃房间、对局、统计、战报和媒体缓存的数据模型。

**Files:**

- Create: `migrations/embed.go`
- Create: `migrations/000001_initial.sql`
- Create: `internal/storage/migration_test.go`

**Tables:**

- `users`
- `rooms`
- `room_players`
- `games`
- `game_players`
- `role_stats`
- `battle_reports`
- `media_cache`
- `bot_update_cursor`

**Steps:**

1. 写从空库执行 Up、检查表/外键/唯一约束、Down 后清空的失败测试。
2. 运行：`go test ./internal/storage -run TestInitialMigration`；预期 FAIL。
3. 用 Goose SQL annotations 编写 migration；`rooms` 只保存活跃房间，结束历史进入 `games`。
4. 房间码、同房用户、座位号和媒体缓存键设置数据库唯一约束。
5. 运行定向测试；预期 PASS。
6. Commit：`feat: add initial SQLite schema`。

## Task 13：配置 sqlc 与类型安全查询

**Objective:** 生成可审查的 SQLite 查询代码。

**Files:**

- Create: `sqlc.yaml`
- Create: `queries/users.sql`
- Create: `queries/rooms.sql`
- Create: `queries/games.sql`
- Create: `queries/reports.sql`
- Create: `queries/media.sql`
- Create: `queries/bot_state.sql`
- Generate: `internal/storage/sqlc/*`
- Test: `internal/storage/queries_test.go`

**Steps:**

1. 先写查询集成测试：用户 upsert、房间原子加入、active 扫描、媒体缓存、战绩读取。
2. 运行：`go test ./internal/storage -run TestQueries`；预期 FAIL。
3. 编写命名 SQL，运行：`go tool sqlc generate`。
4. 生成代码必须提交 Git，禁止手改 generated files。
5. 运行：`go tool sqlc generate && git diff --exit-code -- internal/storage/sqlc`，再跑查询测试。
6. Commit：`feat: add type-safe storage queries`。

## Task 14：实现数据库打开、Pragma 与 migration runner

**Objective:** 用 modernc SQLite 启动一致的 WAL 数据库。

**Files:**

- Create: `internal/storage/db.go`
- Create: `internal/storage/migrate.go`
- Test: `internal/storage/db_test.go`

**Steps:**

1. 测试 `foreign_keys=ON`、WAL、`busy_timeout`、连接关闭和 migration 幂等；强制打开多条 `database/sql` 连接并逐条验证外键与 busy timeout，防止只在初始化连接上执行一次 PRAGMA。
2. 运行：`go test ./internal/storage -run TestOpen`；预期 FAIL。
3. 使用 `modernc.org/sqlite` 打开 DB；通过 modernc 支持的 DSN pragma/连接初始化方式保证每条连接启用外键和 busy timeout；Goose library mode 读取 `migrations.FS`。
4. 配置连接池上限为显式配置；先采用保守默认，后续根据集成测试调整。
5. 运行：`go test -race ./internal/storage`；预期 PASS。
6. Commit：`feat: add SQLite bootstrap and migrations`。

## Task 15：实现用户、房间与启动中止 Repository

**Objective:** 将 P0 生命周期与重启清场封装在事务边界内。

**Files:**

- Create: `internal/storage/users.go`
- Create: `internal/storage/rooms.go`
- Create: `internal/storage/recovery.go`
- Test: `internal/storage/rooms_test.go`
- Test: `internal/storage/recovery_test.go`

**Steps:**

1. 测试原子建房/入房/退房、唯一冲突、座位分配、房主固定 1 号、active 扫描。
2. 运行定向测试；预期 FAIL。
3. 实现 Repository；SQLite error 必须映射为领域错误，不向 Telegram 层泄漏驱动文本。
4. 实现 `ListInterruptedRoomsOnStartup` 和事务化 `MarkInterrupted`，通知由上层 Effect 执行。
5. 运行：`go test -race ./internal/storage`；预期 PASS。
6. Commit：`feat: add room persistence and startup recovery`。

## Task 16：实现对局结算事务与安全备份

**Objective:** 原子保存积分、角色统计、战报和中止记录，并提供一致性备份。

**Files:**

- Create: `internal/storage/settlement.go`
- Create: `internal/storage/backup.go`
- Test: `internal/storage/settlement_test.go`
- Test: `internal/storage/backup_test.go`
- Modify: `cmd/werewolf/main.go`
- Test: `cmd/werewolf/backup_test.go`

**Steps:**

1. 测试事务中任一步失败时全部回滚；胜利、死亡躺赢、失败、恶意退出积分口径正确。
2. 测试在线备份在 WAL 有并发读写时仍可打开；对备份文件显式执行 `PRAGMA integrity_check` 并要求返回 `ok`，离线恢复后关键行数与源库一致。
3. 运行定向测试；预期 FAIL。
4. 实现 `SettleGame(ctx, result)` 单事务；实现 `werewolf backup --output`，先在目标目录生成临时一致性快照、通过 `PRAGMA integrity_check` 后原子改名，禁止裸复制 WAL 主文件。
5. 运行：`go test ./internal/storage ./cmd/werewolf`；预期 PASS。
6. Commit：`feat: add settlement transactions and backup`。

---

# 阶段 M3：Outbox、Telegram Adapter 与应用装配

## Task 17：实现 Outbox 数据模型和按 Chat FIFO

**Objective:** 将游戏 Effects 与 Telegram 网络请求解耦。

**Files:**

- Create: `internal/outbox/message.go`
- Create: `internal/outbox/queue.go`
- Create: `internal/outbox/scheduler.go`
- Test: `internal/outbox/queue_test.go`
- Test: `internal/outbox/scheduler_test.go`

**Steps:**

1. 测试同 Chat FIFO、不同 Chat 可并行、优先级不破坏同 Chat 因果顺序、优雅关闭；队列达到配置上限时返回可观察的 `ErrQueueFull` 并计数，禁止无界吃内存或静默丢消息。
2. 运行：`go test -race ./internal/outbox`；预期 FAIL。
3. 实现每 Chat 队列和全局 ready scheduler；禁止每条消息无限创建 goroutine。
4. 消息携带 correlation ID、room ID、chat ID、operation、priority、coalesce key。
5. 运行 race；预期 PASS。
6. Commit：`feat: add per-chat outbox scheduler`。

## Task 18：实现限速、429 重试、错误分类与更新合并

**Objective:** 抵御广播风暴和 Telegram 临时错误。

**Files:**

- Create: `internal/outbox/limiter.go`
- Create: `internal/outbox/retry.go`
- Create: `internal/outbox/coalesce.go`
- Test: `internal/outbox/limiter_test.go`
- Test: `internal/outbox/retry_test.go`
- Test: `internal/outbox/coalesce_test.go`

**Steps:**

1. Fake transport/clock 测试全局与单 Chat token bucket、`RetryAfter`、最大重试、永久错误不重试；队头消息等待重试期间，同一 Chat 后续消息不得越过它发送，其他 Chat 仍可推进。
2. 测试时间段主消息同 coalesce key 只保留最新待发版本；分页页在超过 3000 后冻结，下一次更新创建顺序编号续页；身份卡、上帝视角实时行动记录、结算战报等不可合并的消息必须保留；重大事件只作为当前时间段主消息内容，不单独产生永久事件消息。
3. 运行：`go test -race ./internal/outbox`；预期 FAIL。
4. 使用 `golang.org/x/time/rate` 实现 limiter；重试时间只从 injected clock 获取。
5. 运行 race；预期 PASS。
6. Commit：`feat: add outbox rate limiting and retry`。

## Task 19：实现 Telegram Client、Long Polling Source 与 Fake API

**Objective:** 封装 `go-telegram/bot`，业务层不依赖框架类型。

**Files:**

- Create: `internal/telegram/client.go`
- Create: `internal/telegram/source.go`
- Create: `internal/telegram/transport.go`
- Create: `internal/telegram/model.go`
- Test: `internal/telegram/client_test.go`
- Test: `internal/telegram/source_test.go`
- Test: `internal/telegram/testserver_test.go`

**Steps:**

1. 用 `httptest.Server` 模拟 `getMe/getUpdates/sendMessage/editMessageText/deleteMessage/sendPhoto/answerCallbackQuery`、409 双实例冲突、403 用户屏蔽、400 不可编辑和 429；断言 `sendPhoto` Caption 使用 MarkdownV2、`answerCallbackQuery` 必须应答且顶部通知使用 `show_alert=false`。
2. 运行：`go test ./internal/telegram`；预期 FAIL。
3. Adapter 在解码 Update 后立即记录服务端 `ReceivedAt`，再转成项目输入 DTO；Transport 将 Outbox operation 转成 Bot API 调用。
4. Long Polling 入口只做轻量解析、去重和入队，显式使用 `WithNotAsyncHandlers()` 或等价单一顺序化 dispatcher 保持 `update_id` 接收顺序；房间间并发交给 Room Actors，而不是框架 handler workers。
5. 定义 `UpdateSource` 接口，仅实现 Long Polling；Webhook 保持未实现。
6. 运行：`go test -race ./internal/telegram`；预期 PASS。
7. Commit：`feat: add Telegram long polling adapter`。

## Task 20：实现 Update 去重与不透明 Callback Token

**Objective:** 防止重复 Update、旧按钮和越权回调执行。

**Files:**

- Create: `internal/telegram/dedupe.go`
- Create: `internal/telegram/callback_tokens.go`
- Create: `internal/telegram/router.go`
- Test: `internal/telegram/dedupe_test.go`
- Test: `internal/telegram/callback_tokens_test.go`
- Test: `internal/telegram/router_test.go`

**Steps:**

1. 测试有界内存 dedupe、并发重复 update ID、Token owner/action/target/phaseVersion、阶段整体失效与 Token map 回收/容量上限；重启测试从 SQLite `bot_update_cursor` 恢复 high-watermark，Actor/Application 已返回“处理成功或明确拒绝”ACK 并提交 cursor 的 Update 不得重放，只有入队但尚未 ACK 的 Update 不得提前推进 cursor。
2. 运行定向测试；预期 FAIL。
3. Token 使用 `crypto/rand` 生成短 base64url 值，回调数据不暴露身份、角色或目标。
4. Router 只产生领域 Command，不直接修改房间状态；单 dispatcher 等待应用层 ACK 后按顺序持久化 update cursor，并在启动时传给 Long Polling initial offset。崩溃窗口允许 Telegram 重投未 ACK Update，领域幂等约束必须安全承受重投。
5. 运行：`go test -race ./internal/telegram`；预期 PASS。
6. Commit：`feat: add idempotent callback routing`。

## Task 21：实现邀请二维码、角色资源 Provider 与 file_id 缓存

**Objective:** 为 P0 邀请二维码和 P1 角色卡发送建立资源边界。

**Files:**

- Create: `internal/telegram/qrcode.go`
- Test: `internal/telegram/qrcode_test.go`
- Create: `assets/embed.go`（仅在实际 PNG 到位后）
- Create: `internal/telegram/media.go`
- Test: `internal/telegram/media_test.go`
- Create: `queries/media.sql`（若 Task 13 尚未包含完整查询则补充）

**Steps:**

1. QR 测试：输入 deep link 产生可解码 PNG bytes；空链接报错。
2. Media 测试：按 Bot ID + SHA-256 查询缓存；命中用 file_id，未命中上传并回写；身份卡图片与完整 Caption 作为同一 `sendPhoto` 消息发送，Caption 长度测试不超过 Telegram 1024 解析字符上限。
3. 运行定向测试；预期 FAIL。
4. 用 `yeqown/go-qrcode/v2` 实现内存 PNG encoder。
5. 角色图片前置条件：`werewolf.png`、`seer.png`、`witch.png`、`villager.png` 必须实际存在后才能添加 `//go:embed role-cards/*.png`；不得提交伪造产品图片。
6. 图片未到位时，自动化测试使用 `testdata` fixture；P1 真实 Bot 验收标记为 blocked，不允许谎报完成。
7. 运行测试；预期 PASS。
8. Commit：`feat: add QR and Telegram media cache`。

## Task 22：实现 App 装配、优雅退出与启动中止通知

**Objective:** 用手动依赖注入串起配置、DB、Outbox、Manager、Telegram 与健康服务。

**Files:**

- Create: `internal/app/app.go`
- Create: `internal/app/build.go`
- Create: `internal/app/shutdown.go`
- Test: `internal/app/app_test.go`
- Modify: `cmd/werewolf/main.go`

**Steps:**

1. Fake components 测试启动顺序、ready 状态、错误回滚、SIGTERM 停止顺序、遗留房间中止通知。
2. 运行：`go test ./internal/app ./cmd/werewolf`；预期 FAIL。
3. 手动构造依赖，不引入 DI 框架；readiness 只有在 migrations 完成、DB ping 成功、Long Polling 未发生 409 冲突、Outbox 与 Room Manager 均可接收工作时才返回 ready。
4. 停止顺序：停止 Update Source → 停止接收新 Command → 停止 Room Actors → drain Outbox（有上限）→ 关 DB/HTTP。
5. 运行：`go test -race ./internal/app ./cmd/werewolf`；预期 PASS。
6. Commit：`feat: wire application lifecycle`。

### M0/M1/M2/M3 Gate

运行：

```bash
gofmt -w $(git ls-files '*.go')
go vet ./...
go test ./...
go test -race ./...
go mod verify
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/werewolf
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/werewolf
git diff --check
```

预期：全部通过；应用可在无真实 Token 的测试模式组装，但真实 Long Polling 尚不执行玩法。

---

# 阶段 P0：建房、邀请、加入与大厅

## Task 23：实现创建房间领域流程

**Objective:** `/newgame` 或主菜单按钮创建唯一 6 人房间，房主固定 1 号。

**Files:**

- Create: `internal/game/lobby.go`
- Create: `internal/game/lobby_commands.go`
- Test: `internal/game/lobby_test.go`
- Create: `internal/telegram/handlers_create.go`
- Test: `internal/telegram/handlers_create_test.go`

**Steps:**

1. 测试默认配置、房主 1 号、重复建房拒绝、6 位去混淆随机码、4～8 位字母数字自定义码、输入大小写混合时统一规范化为大写、重名时明确拒绝且不得偷偷改成随机码、创建 active 记录。
2. 运行定向测试；预期 FAIL。
3. 实现 `CreateRoomCommand` 与 Effects；Adapter 只做输入转换。
4. 主菜单和 `/newgame` 进入相同应用服务，不复制逻辑。
5. 运行测试；预期 PASS。
6. Commit：`feat: add room creation flow`。

## Task 24：实现房间配置、密码与修改截止

**Objective:** 支持 MVP 配置项，并在发牌后锁定。

**Files:**

- Create: `internal/game/settings.go`
- Test: `internal/game/settings_test.go`
- Create: `internal/telegram/handlers_settings.go`
- Test: `internal/telegram/handlers_settings_test.go`
- Modify: `queries/rooms.sql`

**Steps:**

1. 表格测试配置默认值（固定发言 60 秒、狼人夜间 30 秒、其他角色夜间 15 秒）、快速模式减半后的向上取整/5 秒下限、软/固定发言、胜负模式、自救、报身份、空刀、非法组合；密码为 4～16 位英文字母或数字、区分大小写，明文不得入库。
2. 密码测试使用 bcrypt hash，数据库不得保存明文。
3. 发牌开始后所有配置修改必须拒绝。
4. 运行定向测试；预期 FAIL。
5. 实现设置 Command/Reducer/Repository/Telegram 表单按钮。
6. 运行测试；预期 PASS。
7. Commit：`feat: add validated room settings`。

## Task 25：实现邀请、加入、昵称与房间面板

**Objective:** 通过 deep link、二维码或房间码加入，保持房间内昵称唯一。

**Files:**

- Create: `internal/game/join.go`
- Create: `internal/game/nickname.go`
- Test: `internal/game/join_test.go`
- Test: `internal/game/nickname_test.go`
- Create: `internal/telegram/handlers_join.go`
- Create: `internal/telegram/views_lobby.go`
- Test: `internal/telegram/handlers_join_test.go`
- Test: `internal/telegram/views_lobby_test.go`

**Steps:**

1. 测试 deep link、手输代码、密码错误可无限重试、满员、重复加入、退出过同局不可重入、昵称 2～10 字符/允许字符/Unicode NFKC/英文字母大小写无关唯一性/原始大小写显示/随机中文昵称冲突重生/开局锁定。
2. 运行定向测试；预期 FAIL。
3. 默认昵称生成器可注入并处理冲突。
4. 面板显示房间码、人数、成员、状态、开始/设置/解散按钮；邀请消息合并链接、分享按钮和二维码；失效链接必须区分房间已过期、已满和不存在，并给出对应引导。
5. 运行测试；预期 PASS。
6. Commit：`feat: add room join and lobby panel`。

## Task 26：实现大厅退出、移除、房主移交与闲置回收

**Objective:** 完成开局前生命周期和 1 小时房间过期规则。

**Files:**

- Create: `internal/game/lobby_lifecycle.go`
- Test: `internal/game/lobby_lifecycle_test.go`
- Create: `internal/telegram/handlers_lobby.go`
- Test: `internal/telegram/handlers_lobby_test.go`

**Steps:**

1. 测试玩家退出、房主移除玩家、房主退出后按加入顺序移交、旧房主不通知、新房主单独通知。
2. Fake Clock 测试创建后 50 分钟提醒、续期 1 小时、1 小时自动回收、玩家进出不刷新原始期限。
3. 运行定向测试；预期 FAIL。
4. 实现 Commands/Effects，所有面板刷新走 Outbox coalesce。
5. 运行测试；预期 PASS。
6. Commit：`feat: complete lobby lifecycle`。

## Task 27：建立 P0 端到端验收测试

**Objective:** 在 Fake Telegram + 临时 SQLite 中跑通 1 房主 + 5 玩家大厅闭环。

**Files:**

- Create: `internal/app/p0_e2e_test.go`
- Create: `internal/app/testharness_test.go`

**Steps:**

1. 编写从 `/start`、建房、5 次 deep-link 加入、改昵称、面板满员、退出/补位、过期的场景。
2. 运行：`go test ./internal/app -run TestP0EndToEnd -v`；预期先 FAIL。
3. 只修复集成缝隙，不在本任务新增玩法。
4. 断言 SQLite active 行、Outbox 顺序、MarkdownV2 文本和无身份泄漏。
5. 运行：`go test -race ./internal/app -run TestP0EndToEnd -v`；预期 PASS。
6. Commit：`test: cover P0 room flow end to end`。

### P0 Gate

- Fake Telegram 完整通过；
- 真实测试 Bot 可创建房间、生成邀请链接/二维码并让 6 个测试账号入房；
- 不开始发牌；
- `go test -race ./...` 通过。

---

# 阶段 P1：发牌与夜间闭环

## Task 28：实现满员开局、发牌与身份确认

**Objective:** 满员后由房主开始，分配角色并在 10 秒确认后进入第一夜。

**Files:**

- Create: `internal/game/deal.go`
- Test: `internal/game/deal_test.go`
- Create: `internal/telegram/views_role.go`
- Test: `internal/telegram/views_role_test.go`

**Steps:**

1. 测试非满员/非房主/重复开始拒绝，配置锁定，角色分配，狼人队友可见性，角色图片 + Caption 发送，独立确认消息、全员确认和 10 秒超时自动确认；确认消息删除后才创建第 1 夜时间段主消息。
2. 运行定向测试；预期 FAIL。
3. Reducer 产生每玩家私有 Role View Effect；不得产生包含全员身份的公共对象。
4. 角色图缺失时自动化测试只验证 Provider；真实 P1 Gate 必须等待四张正式 PNG。
5. 运行测试；预期 PASS。
6. Commit：`feat: add dealing and role confirmation`。

## Task 29：实现狼人讨论与刀人投票

**Objective:** 两狼在夜间并行讨论和投票，并正确处理平票与超时。

**Files:**

- Create: `internal/game/night_wolf.go`
- Test: `internal/game/night_wolf_test.go`
- Create: `internal/telegram/views_wolf.go`
- Test: `internal/telegram/views_wolf_test.go`

**Steps:**

1. 测试只有存活狼人可讨论/投票；目标为任意存活玩家，明确覆盖自己和狼队友；确认前最终选择可覆盖，确认后锁定；所有存活狼人确认后提前结束。
2. 测试首次平票立即重开讨论/投票并清空确认状态，第二次平票由注入 RNG 随机；超时弃刀、默认必须刀但主动空刀按配置。
3. 测试狼人讨论为独立消息：存活狼人副本在狼人阶段结束后删除，已经处于上帝视角的玩家实时副本永久保留；死亡后不补发死亡前错过的讨论；狼人标识只对有权限查看者可见。
4. 运行定向测试；预期 FAIL。
5. 实现 Commands/Reducer/Views。
6. 运行测试；预期 PASS。
7. Commit：`feat: add werewolf night phase`。

## Task 30：实现女巫解药与毒药阶段

**Objective:** 严格实现女巫每夜一瓶、首夜自救和死亡限制。

**Files:**

- Create: `internal/game/night_witch.go`
- Test: `internal/game/night_witch_test.go`
- Create: `internal/telegram/views_witch.go`
- Test: `internal/telegram/views_witch_test.go`

**Steps:**

1. 表格测试：告知刀口、救/不救、毒/不毒、每一步选择→确认、每夜只能一瓶、药品永久消耗、首夜自救配置；确认后可提前结束阶段。
2. 测试女巫当夜已死亡且不能自救时不能毒；女巫超时不使用任何药。
3. 运行定向测试；预期 FAIL。
4. 实现两个连续决策窗口，但 reducer 保证“一夜一瓶”。
5. 运行测试；预期 PASS。
6. Commit：`feat: add witch night actions`。

## Task 31：实现预言家查验与私有标记

**Objective:** 预言家每夜查验存活玩家，只得到狼人/好人二分结果。

**Files:**

- Create: `internal/game/night_seer.go`
- Test: `internal/game/night_seer_test.go`
- Create: `internal/telegram/views_seer.go`
- Test: `internal/telegram/views_seer_test.go`

**Steps:**

1. 测试目标仅存活玩家、选择→确认查验、确认前可改、确认后结果二分并提前结束、历史查验标记仅预言家可见、超时空验。
2. 测试预言家死亡时阶段仍等待原时长 2/3，但不发操作按钮、不执行技能。
3. 运行定向测试；预期 FAIL。
4. 实现 Commands/Reducer/Viewer-specific View。
5. 运行测试；预期 PASS。
6. Commit：`feat: add seer night action`。

## Task 32：实现夜间结算、死亡与即时胜负判定

**Objective:** 按固定行动顺序结算夜间并立即判断 6 人屠城胜负。

**Files:**

- Create: `internal/game/night_resolve.go`
- Create: `internal/game/victory.go`
- Test: `internal/game/night_resolve_test.go`
- Test: `internal/game/victory_test.go`

**Steps:**

1. 测试刀、救、毒组合，平安夜、多人死亡；在狼人刀人、女巫救/毒和白天投票等每个可能触发胜负的动作完成后立即检查胜负，先触发条件后续行动全部作废，禁止只在“夜末统一结算”时检查一次。
2. 测试 6 人默认屠城：狼人全灭好人胜，好人全灭狼人胜；可配置屠边也按文档判定。
3. 测试死亡角色下一夜进入 2/3 假等待阶段，不泄漏角色是否死亡。
4. 运行定向测试；预期 FAIL。
5. 实现纯结算函数和 victory evaluator。
6. 运行测试；预期 PASS。
7. Commit：`feat: resolve night deaths and victory`。

## Task 33：建立 P1 夜间端到端验收测试

**Objective:** 6 人从满员开局跑过至少两个完整夜晚。

**Files:**

- Create: `internal/app/p1_e2e_test.go`

**Steps:**

1. Scripted RNG 固定牌组，Fake Clock 驱动确认、狼、女巫、预言家窗口。
2. 运行：`go test ./internal/app -run TestP1NightEndToEnd -v`；预期先 FAIL。
3. 断言角色隐私、按钮 owner、Timer 版本、Outbox 顺序、SQLite 仍只有最小 active 数据。
4. 覆盖重启扫描后中止记录和玩家通知。
5. 运行 race；预期 PASS。
6. Commit：`test: cover P1 night flow end to end`。

### P1 Gate

- 4 张正式角色卡 PNG 已放入 `assets/role-cards/` 并通过 embed/file_id 测试；
- 真实测试 Bot 可完成发牌确认和完整夜间；
- 不允许从任何普通玩家视图看到隐藏身份/夜间动作；
- `go test -race ./...` 通过。

---

# 阶段 P2：白天、投票、结算与系统闭环

## Task 34：实现白天死讯与查看者分级视图

**Objective:** 播报夜间结果，并为存活/死亡玩家生成不同视图。

**Files:**

- Create: `internal/game/day_start.go`
- Test: `internal/game/day_start_test.go`
- Create: `internal/telegram/viewer.go`
- Create: `internal/telegram/views_day.go`
- Test: `internal/telegram/viewer_test.go`

**Steps:**

1. 测试默认不报身份/房主配置报身份、公开死讯不含死因、平安夜、死亡玩家统一上帝视角、存活玩家无泄漏。
2. 死亡玩家按时间段补发身份、夜间动作、用药、查验、投票和结果，但不补发死亡前错过的狼人讨论；死亡后产生的讨论实时发送；其普通输入不得广播。
3. 运行定向测试；预期 FAIL。
4. 实现 `ViewerContext` 渲染，禁止复用包含全量秘密的 map 后再删字段；按白天/夜晚时间段为每个 Chat 维护主消息页引用，角色子阶段只编辑该时间段主消息；超过 3000 的成功编辑标记当前页已满，下一次更新创建续页；时间段结束定稿冻结；死亡玩家拥有统一上帝视角第三段和只读行动记录消息。
5. 运行测试；预期 PASS。
6. Commit：`feat: add day announcement views`。

## Task 35：实现麦序、发言限制与消息自毁

**Objective:** 按死者下一位/1 号开始一轮有序发言。

**Files:**

- Create: `internal/game/speech.go`
- Create: `internal/game/speech_limit.go`
- Test: `internal/game/speech_test.go`
- Test: `internal/game/speech_limit_test.go`
- Create: `internal/telegram/handlers_speech.go`
- Test: `internal/telegram/handlers_speech_test.go`

**Steps:**

1. 测试首夜有死者时从死者下一座开始、平安夜从 1 号开始、固定/软限时、结束后不可补充、超时移交、快速模式；发言控制使用独立临时消息，主消息完整累计已接受正文。
2. 测试每回合最多 5 条、每条 50 单位、中英混算、单个连续 ASCII 英文 token 最多 20 个字母、达到 21 个及以上时整条拒绝转播、拒绝反馈。
3. 测试原消息和错误提示约 3 秒后删除，成功转播记录不受删除影响；超时提示也删除。
4. 运行定向测试；预期 FAIL。
5. 实现纯计数器/Tokenizer 和 Commands；删除消息通过延迟 Effect，不在 handler sleep。
6. 运行测试；预期 PASS。
7. Commit：`feat: add ordered daytime speech`。

## Task 36：实现普通投票、弃权和结果公布

**Objective:** 私聊收集最终票，结束后统一公布“谁投了谁”。

**Files:**

- Create: `internal/game/vote.go`
- Test: `internal/game/vote_test.go`
- Create: `internal/telegram/views_vote.go`
- Test: `internal/telegram/views_vote_test.go`

**Steps:**

1. 测试候选人、选择→确认投票、确认前改票、确认后锁定、弃权确认、全部有票权玩家确认后提前结束、超时弃权、投票阶段静默、死亡玩家无票权。
2. 投票过程中任何玩家不得看到实时票数；结算后统一显示票向明细。
3. 测试遗言绑定“不报身份”模式：默认不报身份时被票死者有 30 秒遗言；房主选择报身份时无遗言；狼人自爆永远无遗言；投票临时消息结束后删除，明细/统计/结果写入当天主消息。
4. 运行定向测试；预期 FAIL。
5. 实现 Vote Command/Reducer/Effect 与遗言子阶段。
6. 运行测试；预期 PASS。
7. Commit：`feat: add anonymous daytime voting`。

## Task 37：实现完整平票流程

**Objective:** 按权威文档实现加时发言、多轮缩圈和最终二人对决。

**Files:**

- Create: `internal/game/tie_vote.go`
- Test: `internal/game/tie_vote_test.go`

**Steps:**

1. 表格测试首次平票加时发言、第二轮仅平票候选、平票人也投其他人。
2. 测试 ≥3 人继续无发言投票，最多 2 轮后随机保留 2 人。
3. 测试最终 2 人不投票、其他人禁止弃权；偶数投票人随机排除 1 人后必然决胜。
4. 运行：`go test ./internal/game -run TestTieVote`；预期 FAIL。
5. 用注入 RNG 实现兜底随机，所有分支保持可重放测试。
6. 运行测试；预期 PASS。
7. Commit：`feat: add deterministic tie vote resolution`。

## Task 38：实现狼人白天自爆与恶意退出

**Objective:** 白天自爆优先打断发言/投票并进入黑夜，夜间退出按死亡处理。

**Files:**

- Create: `internal/game/explode.go`
- Create: `internal/game/leave.go`
- Test: `internal/game/explode_test.go`
- Test: `internal/game/leave_test.go`

**Steps:**

1. 测试白天任意时机自爆、无遗言、已有投票作废、直接进黑夜。
2. 测试狼人白天主动退出按自爆；夜间退出按恶意退出死亡；自爆、猎人开枪、恶意退出只写当前时间段主消息，不额外发送永久事件消息。
3. 测试游戏进行中存活玩家主动退出、连续 3 次超时被系统强制移除、游戏中被投票踢出均触发 10 分钟冷却，冷却期间不能创建或加入房间；正常死亡/结算/中止等不触发。
4. 测试玩家离开某局后不能重新加入该局、连续 3 次超时被移除和私聊预警。
5. 运行定向测试；预期 FAIL。
6. 实现 Commands/Reducer/Effects。
7. 运行测试；预期 PASS。
8. Commit：`feat: add wolf explosion and malicious leave`。

## Task 39：实现投票解散、投票踢人与房主控制

**Objective:** 完成游戏中的治理机制和发起次数限制。

**Files:**

- Create: `internal/game/governance.go`
- Test: `internal/game/governance_test.go`
- Create: `internal/telegram/views_host.go`
- Test: `internal/telegram/views_host_test.go`

**Steps:**

1. 测试仅存活玩家参与、超过三分之一通过、每人每局一次、每阶段一次。
2. 测试踢人走掉线规则；投票解散不扣分；房主强制解散二次确认并扣 10 分，积分 ≤9 禁止。
3. 房主控制按钮与普通游戏按钮分开展示。
4. 运行定向测试；预期 FAIL。
5. 实现治理状态与 Commands。
6. 运行测试；预期 PASS。
7. Commit：`feat: add in-game governance`。

## Task 40：实现结算、积分、战报与再来一局

**Objective:** 完成一局后原子结算并回到等待大厅。

**Files:**

- Create: `internal/game/settlement.go`
- Create: `internal/game/report.go`
- Test: `internal/game/settlement_test.go`
- Test: `internal/game/report_test.go`
- Create: `internal/telegram/views_result.go`
- Test: `internal/telegram/views_result_test.go`

**Steps:**

1. 测试胜方、全员翻牌、胜利 +5、死亡躺赢 +2、失败 0、恶意退出失败 -5，并确认 MVP 不计算或展示 MVP 玩家；当前时间段主消息先定稿，独立结算战报永久发送。
2. 测试战报包含参与人、角色、结果和关键事件，但不伪装成完整回放。
3. 测试回大厅保留成员、沿用配置、点击“再来一局”后至少 15 秒退出窗口、配置可重新修改；再来一局/退出/房间面板使用独立临时大厅控制消息，战报不挂旧按钮。
4. 运行定向测试；预期 FAIL。
5. Reducer 产生 `PersistSettlementEffect`，外层调用 Task 16 的单事务。
6. 运行测试；预期 PASS。
7. Commit：`feat: add scoring reports and rematch`。

## Task 41：实现玩家命令与帮助入口

**Objective:** 完成 MVP 对外命令面。

**Files:**

- Create: `internal/telegram/commands.go`
- Create: `internal/telegram/handlers_commands.go`
- Create: `internal/i18n/locales/active.zh-CN.yaml`（补全）
- Test: `internal/telegram/handlers_commands_test.go`

**Commands:**

- `/start`
- `/newgame`
- `/join`
- `/role`
- `/score`
- `/leave`
- `/help`

`/rank` 只返回“后续开放”说明，不实现排行榜数据。

**Steps:**

1. 测试命令解析、私聊限定、无房/游戏中/死亡状态反馈和 MarkdownV2 escaping。
2. 运行定向测试；预期 FAIL。
3. 所有命令调用现有 application services，不复制 reducer 逻辑。
4. 补全中文帮助、新手规则和首次发言 3 秒自毁提示。
5. 运行测试；预期 PASS。
6. Commit：`feat: add MVP player commands`。

## Task 42：建立完整 6 人局端到端模拟

**Objective:** 不连接真实 Telegram，在测试中从建房一直跑到结算和再来一局。

**Files:**

- Create: `internal/app/mvp_e2e_test.go`
- Create: `internal/app/scenarios_test.go`
- Create: `testdata/scenarios/good_win.yaml`
- Create: `testdata/scenarios/wolf_win.yaml`
- Create: `testdata/scenarios/tie_vote.yaml`
- Create: `testdata/scenarios/restart_abort.yaml`

**Steps:**

1. 场景 A：好人胜；覆盖女巫救/毒、预言家查验、白天投狼。
2. 场景 B：狼人胜；覆盖超时与恶意退出。
3. 场景 C：复杂平票与最终二人对决。
4. 场景 D：进程重启中止，不结算积分但保留记录并通知。
5. 运行：`go test ./internal/app -run TestMVPEndToEnd -v`；预期先 FAIL。
6. 只修复集成缺口；断言无 goroutine 泄漏、Outbox FIFO、数据库最终状态和所有视图隐私。
7. 运行：`go test -race ./internal/app -run TestMVPEndToEnd -v`；预期 PASS。
8. Commit：`test: cover complete six-player games`。

---

# 阶段 R：质量门禁、发布与真实验收

## Task 43：实现 GitHub Actions PR 质量流水线

**Objective:** 将终审质量门禁变成合并前强制检查。

**Files:**

- Create: `.github/workflows/ci.yml`
- Create: `.github/dependabot.yml`
- Modify: `Makefile`

**Jobs:**

1. `format-vet-lint`
2. `unit-integration`
3. `race`（原生 Linux，可 CGO=1）
4. `migration-sqlc`
5. `vulnerability`
6. `build-matrix`（linux/amd64、linux/arm64，CGO=0）

**Steps:**

1. 先在分支故意制造格式或 sqlc 差异，验证 job 会失败；随后恢复。
2. CI 固定 Go `1.25.x` 和工具版本，不使用浮动 latest；第三方 GitHub Actions 固定到审核过的 commit SHA，并配置最小 `permissions`。
3. `migration-sqlc` 从空库跑 migrations，并执行 `go tool sqlc generate && git diff --exit-code`。
4. 运行本地等价命令：`make check`；预期 PASS。
5. Commit：`ci: enforce Go quality gates`。

## Task 44：实现 Release、GHCR 和多架构镜像

**Objective:** tag 时由云端生成二进制、校验和和容器镜像，本机不构建 Docker。

**Files:**

- Create: `.github/workflows/release.yml`
- Create: `.goreleaser.yaml`
- Create: `Dockerfile`
- Create: `deploy/systemd/telegram-werewolf.service`
- Create: `deploy/config.example.yaml`

**Steps:**

1. GoReleaser 配置 linux/amd64、linux/arm64、checksums，构建环境 `CGO_ENABLED=0`。
2. Dockerfile 使用 CI 产出的二进制或多阶段云端构建；运行时使用非 root 用户和外部 `/data`。
3. systemd unit 使用专用非登录用户、`StateDirectory`/`ReadWritePaths`、`EnvironmentFile`、`NoNewPrivileges=true`、`PrivateTmp=true` 和只读系统目录；只给 SQLite 数据目录写权限。
4. Release workflow 仅在 `v*` tag 触发 Buildx 并推送 GHCR；PR 不构建镜像；job 仅在发布步骤授予 `contents: write`/`packages: write`。
5. 运行只读校验：GoReleaser config check、workflow YAML lint；本机不执行 Docker build。
6. Commit：`ci: add release and GHCR pipeline`。

## Task 45：更新项目文档与运维手册

**Objective:** 让新开发者能从零运行、测试、备份和排障。

**Files:**

- Modify: `README.md`
- Create: `docs/开发指南.md`
- Create: `docs/部署与运维.md`
- Create: `docs/测试验收清单.md`

**Steps:**

1. README 更新当前状态、技术文档链接、快速开始、测试命令和仓库结构。
2. 开发指南写 Go/tool 版本、配置、sqlc、migration、Fake Bot API、分支/提交约定。
3. 运维文档写 systemd、数据目录、健康检查、备份/恢复、重启中止语义和日志脱敏。
4. 验收清单逐条映射 `docs/游戏流程设计.md` 的 MVP 规则。
5. 运行 Markdown 本地链接检查和 `git diff --check`。
6. Commit：`docs: add development and operations guides`。

## Task 46：真实 Telegram Bot 冒烟与发布候选验收

**Objective:** 用专用测试 Bot 和 6 个测试账号验证真实平台行为。

**Files:**

- Modify only if defects found: corresponding source/test files
- Record evidence: `docs/测试验收清单.md`

**Steps:**

1. 在安全环境设置 `TELEGRAM_BOT_TOKEN`，不得写入 shell history、日志或仓库。
2. 验证 `/start`、建房、deep link、二维码、6 人加入、身份卡、两夜两昼、投票与结算。
3. 验证 MarkdownV2 特殊昵称、3 秒删除、阶段主消息编辑、429 模拟/真实退避和屏蔽 Bot 错误分类。
4. 杀死进程后重启：确认未结束局中止、通知玩家、不加减积分。
5. 执行备份、完整性检查、离线恢复并回读积分/战报。
6. 发现缺陷时：先写失败回归测试，再修复，再重复对应冒烟步骤。
7. 最终运行：

```bash
make check
go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/werewolf
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/werewolf
git status --short
```

8. 只有全部通过且工作树干净时，创建 release candidate tag。
9. Commit（仅验收文档或缺陷修复）：`test: complete Telegram MVP acceptance`。

---

## 3. 阶段验收矩阵

| 阶段 | 用户可见成果 | 自动化 Gate | 明确不做 |
|---|---|---|---|
| M0-M3 | 可启动应用骨架、DB、Outbox、健康检查 | 全量单测/race/交叉编译 | 玩法 |
| P0 | 6 人建房、邀请、加入、面板 | P0 E2E + SQLite + Fake Telegram | 发牌 |
| P1 | 发牌与完整夜间 | P1 E2E + 隐私断言 | 白天投票 |
| P2 | 完整 6 人局、积分、战报、再来一局 | 多场景 MVP E2E + race | 猎人/守卫/AI/观战/排行榜 |
| R | CI、Release、systemd、GHCR、真实冒烟 | GitHub checks + 人工验收 | 本机 Docker 构建 |

---

## 4. 关键风险与强制验证

### 身份泄漏

- 风险：复用含全量秘密的数据结构再“删字段”可能漏删。
- 约束：从 `ViewerContext` 白名单生成视图；每种角色/生死状态均做快照测试。

### 重复结算

- 风险：Timer、按钮与网络重投同时到达。
- 约束：`update_id` 去重 + callback Token + `phaseVersion` + Reducer 终态幂等测试。

### Outbox 乱序

- 风险：不同优先级或重试让同一玩家后发先至。
- 约束：同 Chat 严格 FIFO；只合并尚未发送且具有相同 coalesce key 的阶段更新。

### SQLite 竞争

- 风险：多房间结算同时写入触发 busy。
- 约束：WAL、`busy_timeout`、显式事务、受控连接池；并发集成测试验证。

### MarkdownV2 发送失败

- 风险：玩家昵称/发言包含控制字符。
- 约束：统一 escaper、Fuzz、Fake Bot API payload 断言，禁止 handler 手拼动态 Markdown。

### 角色图片缺失

- 风险：当前 `assets/role-cards/` 只有 README，`go:embed role-cards/*.png` 会因无匹配文件导致编译失败。
- 约束：正式 PNG 到位前不创建生产 embed directive；P1 真实验收保持 blocked，不能用虚假成功替代。

### Race Detector 与纯 Go 发布差异

- 风险：`go test -race` 需要原生工具链环境，而发布要求 CGO=0。
- 约束：CI 拆成 race job（原生 Linux）和 release build job（CGO=0），两者都必须通过。

---

## 5. 已确认产品常量

以下 5 项产品常量已经通过文字 Q&A 确认，并已回写 [游戏流程设计.md](../../docs/游戏流程设计.md) 与 [设计Q&A.md](../../docs/设计Q&A.md)。对应实现任务不再阻塞：

1. 自定义房间码不区分大小写，统一转为大写存储和显示。
2. 房间密码为 4～16 位英文字母或数字，区分大小写；不允许空格、中文和特殊符号。
3. 游戏昵称为 2～10 个字符，仅允许中文汉字、英文字母和数字；输入做 Unicode NFKC 规范化，英文字母比较不区分大小写，默认随机昵称为「中文形容词 + 动物/物品」。
4. 单个连续 ASCII 英文 token 最多 20 个字母，达到 21 个及以上时整条发言拒绝转播。
5. 恶意退出相关行为触发 10 分钟跨局加入冷却；正常退出、死亡、结算、再来一局、解散和 Bot 中止不触发。

---

## 6. 开放前置条件

不阻塞 M0/P0 自动化开发：

- GitHub 仓库已创建：`https://github.com/v2up-32mb/telegram-werewolf`；
- 本地 `main` 已跟踪 `origin/main`；
- Go 1.25.5 可用。

进入 P1 真实 Bot Gate 前必须具备：

1. 专用测试 Bot Token（仅环境变量）；
2. 四张正式 MVP 角色卡 PNG；
3. 6 个可用于人工验收的 Telegram 测试账号，或用户明确接受人工步骤由多人协作完成。

生产发布前才需要：

- 最终服务器与数据目录；
- systemd 用户、备份调度与远端备份目标；
- 是否启用 Webhook（不阻塞 Long Polling 首发）。

---

## 7. 完成定义（Definition of Done）

MVP 只有同时满足以下条件才算完成：

- 两场不同胜方的 6 人局 Fake Telegram E2E 通过；
- 复杂平票、重启中止和恶意退出场景通过；
- 所有隐藏信息可见性测试通过；
- `go test ./...`、`go test -race ./...`、Fuzz smoke、migration smoke 全通过；
- sqlc 重新生成后工作树无差异；
- linux/amd64、linux/arm64 的 `CGO_ENABLED=0` 构建通过；
- GitHub Actions 所有 required checks 通过；
- 真实 Telegram 冒烟通过；
- 备份、完整性检查和恢复回读通过；
- 文档与实际命令一致；
- 工作树干净，Release/镜像均可追溯到同一 Git tag。

---

## Sources

[1] https://api.github.com/repos/go-telegram/bot — go-telegram/bot repository metadata

[2] https://github.com/go-telegram/bot/releases/tag/v1.23.0 — go-telegram/bot v1.23.0

[3] https://api.github.com/repos/yeqown/go-qrcode — yeqown/go-qrcode repository metadata

[4] https://github.com/yeqown/go-qrcode/releases/tag/v2.2.5 — yeqown/go-qrcode v2.2.5

[5] https://api.github.com/repos/pressly/goose — pressly/goose repository metadata

[6] https://github.com/pressly/goose/releases/tag/v3.27.3 — pressly/goose v3.27.3

[7] https://raw.githubusercontent.com/pressly/goose/master/README.md — pressly/goose README
