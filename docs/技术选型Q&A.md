# Telegram 狼人杀 Bot —— 技术选型 Q&A

> 版本：v0.1（技术终审记录）
> 状态：28 项技术决策已全部确认
> 确认日期：2026-08-15
> 最终结论：见 [技术选型.md](技术选型.md)
> 说明：本文记录技术方案的备选项、最终选择和理由；不定义玩法规则。

---

## Q1. MVP 最终部署在哪里？

**备选方案：**

- 当前本机 Debian/ARM64；
- `tokyo-01` 云服务器；
- 另选云服务器/VPS；
- 先本机开发，生产环境暂不确定。

**最终选择：** 先在本机开发，生产部署环境暂不确定。

**理由与约束：**

- 技术方案不能绑定某一台服务器。
- 本机必须能完成开发、测试和原生构建。
- 交付物应同时覆盖 Linux amd64 和 arm64。
- 生产主机、域名、反向代理与公网入口后续再决定。

---

## Q2. 核心开发语言与 Bot 框架走哪条路线？

**备选方案：**

- Go：低资源、并发稳定、单文件部署；
- Python + aiogram：优先 MVP 开发速度；
- TypeScript + grammY：类型安全与开发效率平衡；
- 暂不指定，后续推荐。

**最终选择：** Go。

**理由：**

- 多房间、阶段计时、事件排序和发送队列与 Go 并发模型契合。
- 低内存、纯 Go 交叉编译和单二进制部署符合项目目标。
- 强类型有利于维护复杂游戏状态和角色规则。

---

## Q3. MVP 的状态与数据存储采用什么方案？

**备选方案：**

- 内存状态机 + SQLite 最小快照；
- 全部游戏状态以 SQLite 为准；
- 从一开始使用 PostgreSQL；
- 等并发规模明确后再选。

**最终选择：** 内存状态机 + SQLite 最小快照。

**理由：**

- 进行中的阶段、投票、角色操作和计时器适合由房间内存状态机管理。
- 产品已明确重启后不续局，无需为完整恢复建设重型持久化。
- 玩家、积分、战绩、战报和中止记录仍需长期保存。
- SQLite 还必须保留足以在重启后识别参与者、生成中止记录并通知玩家的最小活跃局数据。

---

## Q4. Telegram Bot 的 Update 接收方式怎么选？

**备选方案：**

- MVP 只使用 Long Polling；
- Long Polling 为主，预留 Webhook 配置入口；
- 第一版同时完整支持 Long Polling 和 Webhook；
- 只使用 Webhook。

**最终选择：** Long Polling 为主，同时预留 Webhook 配置入口。

**理由：**

- 本机开发不需要公网域名、HTTPS 和开放端口。
- 生产环境未确定，不应提前背负 Webhook 运维约束。
- 业务层不与 Long Polling 循环耦合，未来可增加 Webhook Adapter。
- “预留”不代表 MVP 同时维护两套生产接入链路。

---

## Q5. Go 的 Telegram Bot 框架选择哪个？

**主要候选：**

1. `github.com/go-telegram/bot`
2. `github.com/mymmrac/telego`
3. 先为两个库分别做最小原型后再决定
4. 暂不确定框架

**最终选择：** `github.com/go-telegram/bot`。

**调研结论：**

- `go-telegram/bot` 的 API 边界较薄、零第三方依赖，提供 Long Polling、Webhook、中间件、Update workers 和 429 错误信息；选型时最新 `v1.23.0` 已支持 Telegram Bot API 10.2。[1][2]
- Telego 同样处于维护状态，支持 Bot API 10.2，并提供更厚的 handler groups、middleware、工具构建器和 conversation 示例，但其部分框架能力会与本项目自己的状态机重叠。[3][4]

**选择理由：**

- Telegram 库只承担 Bot API、Update 接收和基础路由。
- 游戏状态、房间并发、计时器与消息调度由项目自行掌握。
- 更符合低依赖、低资源和可控演进目标。

---

## Q6. MVP 的整体进程架构怎么选？

**备选方案：**

- 单进程模块化单体；
- Bot 接入与游戏引擎拆成两个进程；
- 微服务架构；
- 暂不决定。

**最终选择：** 单进程、单二进制的模块化单体。

**理由：**

- MVP 规模没有证明需要分布式架构。
- 单进程更容易保证状态、计时与消息调度的一致性。
- 模块通过接口隔离，未来有真实容量依据时仍可拆分。
- 不引入 Redis、RabbitMQ 或独立 worker。

---

## Q7. SQLite 驱动与数据访问方式怎么选？

**备选方案：**

- `modernc.org/sqlite` + `database/sql` + `sqlc` + SQL migrations；
- `modernc.org/sqlite` + 手写 `database/sql`；
- GORM + SQLite；
- 暂不决定。

**最终选择：** `modernc.org/sqlite` + `database/sql` + `sqlc` + 纯 SQL migrations。

**理由：**

- `modernc.org/sqlite` 为纯 Go 驱动，适合 `CGO_ENABLED=0` 与 amd64/arm64 交叉编译。
- `sqlc` 从真实 SQL 生成类型安全代码，没有 ORM 运行时反射。
- 普通 SQL migration 易审查、易测试。
- 项目数据模型适合明确控制查询与事务，不需要 GORM 自动推断。

---

## Q8. 实时对局的并发与顺序一致性采用哪种模型？

**备选方案：**

- 每房间一个 goroutine + command channel；
- 共享状态 + 每房间 mutex；
- 所有房间共用一个全局事件循环；
- 引入第三方 Actor 框架。

**最终选择：** 每个活跃房间一个 goroutine，并通过 command channel 串行处理输入。

**理由：**

- 同房间内的发言、投票、技能、超时和管理操作天然需要严格排序。
- 房间 goroutine 独占可变状态，避免到处加锁。
- 不同房间仍可并行。
- 无需引入第三方 Actor 框架。

---

## Q9. Telegram 消息发送与限流采用哪种架构？

**备选方案：**

- 进程内 Outbox + FIFO + 限速重试；
- 各 handler 直接调用 Telegram API；
- 每个房间独立发送 worker，不设全局调度；
- Redis/RabbitMQ 持久队列。

**最终选择：** 进程内 Outbox 调度器。

**必须具备：**

- 按聊天 FIFO；
- 全局与单聊天限速；
- 消息优先级；
- 低优先级滚动更新的合并或覆盖；
- 429 `RetryAfter` 自动退避重试；
- 可观测的队列、失败与重试计数。

**理由：** 房间 Actor 不应因 Telegram 网络延迟或限流阻塞；MVP 的重启策略也不需要外部持久消息队列。

---

## Q10. 游戏阶段与房间超时采用什么计时机制？

**备选方案：**

- 房间级 `time.Timer` + 阶段版本校验；
- 全局最小堆/时间轮；
- 第三方定时任务框架；
- SQLite 轮询。

**最终选择：** 房间级 `time.Timer` + `phaseVersion`。

**理由：**

- 每次阶段切换递增版本。
- Timer 到期只投递 Timeout Command，Actor 再校验阶段和版本。
- 已停止但竞争触发的旧 Timer 不会重复结算。
- 不需要复杂时间轮、cron 库或数据库轮询。

---

## Q11. 狼人杀核心规则引擎采用哪种建模方式？

**备选方案：**

- 显式状态机 + Command/Effect Reducer；
- Actor 内直接修改状态并执行副作用；
- 第三方 FSM 库；
- 完整 Event Sourcing。

**最终选择：** 强类型显式状态机 + Command/Effect Reducer。

**核心约束：**

```text
新状态 + Effects = Reduce(旧状态, Command)
```

- Reducer 不直接访问 Telegram、SQLite、系统时间或全局随机源。
- 外层负责执行发消息、计时器、存储等 Effects。
- 不建设完整事件溯源；战报只记录产品需要的领域事件。

**理由：** 便于对复杂角色规则、平票与死亡链做确定性表格测试，同时避免事件溯源过度设计。

---

## Q12. 应用配置与 Bot Token 如何管理？

**备选方案：**

- YAML + 环境变量密钥覆盖；
- 全部使用环境变量；
- 全部写入 YAML；
- Viper/Cobra 统一管理。

**最终选择：** YAML 保存非敏感配置，环境变量保存密钥并可覆盖配置。

**规则：**

- 本地开发可使用不提交 Git 的 `.env`。
- 启动时显式校验关键配置。
- 房主游戏规则存数据库，不与系统配置混用。
- 不引入大一统配置框架。

---

## Q13. MVP 的日志与可观测性采用什么方案？

**备选方案：**

- `slog` + 健康检查 + 轻量计数器；
- `slog` + Prometheus；
- 完整 OpenTelemetry；
- 普通文本日志。

**最终选择：** Go 标准库 `log/slog` + `/healthz`、`/readyz` + 轻量计数器。

**规则：**

- 开发输出文本，生产输出 JSON。
- 日志携带房间、对局、阶段与命令字段。
- 不记录 Token、完整私聊内容或隐藏身份。
- 统计活跃房间、Outbox 长度、失败、429、重试和超时。
- MVP 不绑定完整外部监控平台。

---

## Q14. MVP 的自动化测试策略采用什么方案？

**备选方案：**

- 完整分层测试 + Fake Telegram + Race Detector；
- 只测试 Reducer 与数据库；
- CI 连接真实 Telegram 测试 Bot；
- 功能完成后再补测试。

**最终选择：** 完整分层自动化测试。

**范围：**

1. Reducer 表格驱动测试；
2. Go Fuzz 随机 Command 序列；
3. Fake Clock 驱动 Actor 与 Timer；
4. 临时 SQLite 数据库集成测试；
5. `httptest` 模拟 Telegram Bot API；
6. 429、超时、重试、顺序和幂等测试；
7. CI 执行 `go test -race ./...`；
8. 真实 Bot 仅作发布前人工冒烟。

---

## Q15. 项目的构建与部署交付形式怎么选？

**备选方案：**

- 原生跨平台二进制为主，Docker 为辅；
- 只发布原生二进制 + systemd；
- 只提供 Docker；
- 等生产服务器确定后再选。

**最终选择：** 原生跨平台二进制为主，Docker 为辅，并增加以下明确约束：

- 发布 Linux amd64 和 arm64 二进制。
- 提供 systemd unit 模板。
- 本地开发不构建 Docker 镜像。
- Docker 多架构镜像仅由云端 CI 构建。
- SQLite、配置与日志使用外部数据目录。

**理由：** 保持低资源原生部署优势，同时为未来云端部署保留容器选择；避免在本地 ARM64 设备消耗磁盘和构建时间。

---

## Q16. 云端 CI、二进制发布与镜像仓库采用哪套平台？

**备选方案：**

- GitHub Actions + GitHub Releases + GHCR；
- GitHub Actions + GitHub Releases + Docker Hub；
- Gitea Actions + 自建仓库；
- 暂不绑定平台。

**最终选择：** GitHub Actions + GitHub Releases + GHCR。

**理由：** 源码、PR、CI、二进制 Release 与容器镜像可在同一平台管理。当前本地 Git 仓库尚未配置远端，远端创建属于后续执行事项。

---

## Q17. 多语言文案层采用什么技术方案？

**备选方案：**

- `go-i18n/v2` + `go:embed`，MVP 仅 `zh-CN`；
- 自研简单字典；
- MVP 硬编码中文；
- 跟随 Telegram 客户端语言。

**最终选择：** `go-i18n/v2` + `go:embed`，MVP 仅交付 `zh-CN`。

**理由：**

- 用户文案不散落在 handler 中。
- Reducer 只产生消息 key 与参数。
- 后续增加英语、日语、韩语和俄语时不修改核心引擎。
- 成熟本地化库可处理俄语等复杂复数规则。

---

## Q18. Bot 消息使用什么富文本格式？

**备选方案：**

- Telegram HTML + 严格转义；
- Telegram MarkdownV2；
- 全部纯文本；
- 混用 HTML 与 MarkdownV2。

**最终选择：** 统一使用 Telegram MarkdownV2。

**补充约束：**

- 所有动态值必须经过统一 MarkdownV2 escaper。
- 业务代码不得手写转义或直接拼接不可信输入。
- 玩家昵称和发言只能作为转义后的文字插入。
- 不混用 HTML ParseMode。

---

## Q19. 角色卡图片等静态资源如何交付和发送？

**备选方案：**

- `go:embed` 内嵌图片 + SQLite 缓存 Telegram `file_id`；
- 部署时保留 assets 目录 + 缓存 `file_id`；
- 每次发送重新上传；
- 外部 CDN URL。

**最终选择：** `go:embed` 内嵌图片，并缓存 Telegram `file_id`。

**规则：**

- 源文件继续放在 `assets/role-cards/`。
- 首次发送上传图片并缓存 `file_id`。
- 缓存键包含 Bot ID 与资源内容哈希。
- 更换 Bot 或图片更新后自动重新上传。

---

## Q20. 项目的 Go 版本基线与构建模式怎么定？

**已核实本机环境：** Go 1.25.5、Linux/ARM64、CGO 关闭。

**备选方案：**

- Go 1.25 基线 + CI 1.25.x + CGO 关闭；
- 兼容到 Go 1.24；
- CI 永远使用 latest stable；
- 暂不固定版本。

**最终选择：** Go 1.25 基线，CI 使用最新 1.25.x，正式构建 `CGO_ENABLED=0`。

**补充说明：** Race Detector CI job 可在原生 Linux 环境启用 CGO；正式发布产物仍保持纯 Go。

---

## Q21. AI 补位在当前技术方案中做到什么程度？

**备选方案：**

- 只预留 `PlayerController`，不接模型；
- MVP 实现简单规则 AI；
- MVP 直接接 OpenAI 兼容 LLM；
- 完全不预留。

**最终选择：** MVP 只预留 `PlayerController`，不接任何 AI 模型。

**理由：**

- 真人和未来 AI 都通过统一 Command 驱动引擎。
- 避免将模型供应商、提示词、成本与超时引入核心架构。
- AI 角色策略仍需后续单独完成玩法 Q&A。

---

## Q22. 发牌、随机目标与房间码采用什么随机方案？

**备选方案：**

- 服务端 `crypto/rand`；
- 哈希承诺与结束后公开种子；
- 多人共同提供种子；
- `math/rand`。

**最终选择：** 服务端密码学安全随机。

**规则：**

- 使用 `crypto/rand`。
- 发牌采用无偏 Fisher–Yates。
- 房间码使用密码学随机并由数据库保证当前唯一。
- 注入 `RNG` 接口以实现确定性测试。
- MVP 不实现可验证随机协议。

---

## Q23. MVP 是否需要额外 Web 管理后台？

**备选方案：**

- 不做 Web 管理后台；
- 只读状态页；
- 完整管理员后台；
- 预留管理 API。

**最终选择：** 不开发 Web 管理后台，也不预留管理 API。

**理由：**

- 玩家和房主交互全部位于 Telegram。
- HTTP 仅提供健康检查。
- 避免引入前端、管理员鉴权、审计 API 和额外攻击面。

---

## Q24. SQLite 的备份与恢复怎么设计？

**备选方案：**

- 应用提供安全备份命令，调度交给部署环境；
- 应用内部每天定时生成备份；
- 直接集成 S3/WebDAV；
- MVP 不提供备份。

**最终选择：** 应用负责一致性备份、恢复和完整性检查，调度与远端同步交给部署环境。

**规则：**

- 提供类似 `werewolf backup --output <path>` 的 CLI。
- 使用 SQLite 在线备份能力，不直接复制正在运行的 WAL 主文件。
- 恢复后执行完整性检查。
- 不在应用中绑定 S3、WebDAV 或 rclone。

---

## Q25. 按钮回调与重复 Update 如何保证安全和幂等？

**备选方案：**

- 阶段级不透明 Token + Update 去重；
- 结构化 callback data + HMAC；
- 明文 callback data + 服务端校验；
- 完全依赖 Telegram。

**最终选择：** 阶段级不透明随机 Token + `update_id` 有界去重。

**规则：**

- Token 映射允许用户、动作、目标、阶段与 `phaseVersion`。
- 阶段切换时旧 Token 整体失效。
- Actor 再次校验玩家与目标权限。
- 允许修改最终选择的操作按规则覆盖，不强制单次点击即销毁。
- 重复 `update_id` 不得重复执行。

---

## Q26. 进行中房间的最小快照保存到什么粒度？

**备选方案：**

- 关系表保存中止清场信息，不保存完整状态；
- 每个 Command 后序列化完整游戏状态 JSON；
- 只保存房间 ID；
- 记录完整事件流。

**最终选择：** 使用关系表保存中止、通知和留档所需的最小信息。

**概念表：**

- `active_rooms`；
- `active_room_players`；
- 长期 `games`、战报、玩家、积分与统计表。

**运行规则：**

- 建房、入房、退房和开局时更新最小记录。
- 投票和技能 Command 不持续写完整状态。
- 正常结算在事务中写入长期数据并清除 active 标记。
- 启动时把遗留 active 对局标记为中止并通知参与者。
- SQLite 启用 WAL、外键和 `busy_timeout`。

---

## Q27. Go 项目的目录与依赖组织采用什么风格？

**备选方案：**

- 按领域模块组织的 Ports & Adapters + 手动依赖注入；
- 严格四层 Clean Architecture；
- MVC；
- handlers/services/models 技术类型分包。

**最终选择：** 领域模块化 Ports & Adapters，并通过普通构造函数手动注入。

**核心边界：**

```text
cmd/werewolf/
internal/game/
internal/room/
internal/telegram/
internal/outbox/
internal/storage/
internal/player/
internal/i18n/
internal/config/
internal/observability/
migrations/
queries/
assets/role-cards/
```

**规则：**

- 核心 `game` 不依赖 Telegram、SQLite 或配置库。
- 不引入 Wire、Fx 等 DI 框架。
- 不机械制造无业务价值的接口、DTO 和分层。

---

## Q28. CI 的工程质量门禁做到什么程度？

**备选方案：**

- 完整但务实的 Go 质量门禁；
- 只跑标准工具、测试和编译；
- 测试优先但无 lint/漏洞扫描；
- 不设强制门禁。

**最终选择：** 完整质量门禁。

每个 PR 必须通过：

```text
gofmt
go vet ./...
固定版本 golangci-lint
govulncheck ./...
go mod verify
go test ./...
go test -race ./...
SQLite migrations 从空库执行
sqlc generate 后 Git 工作树无变化
linux/amd64 交叉编译
linux/arm64 交叉编译
```

**生成与发布规则：**

- `sqlc` 生成代码提交 Git，并由 CI 重新生成校验。
- 普通 PR 不构建或推送 Docker 镜像。
- tag/release 由 GitHub Actions 构建多架构原生二进制和 GHCR 镜像。
- 本地开发不构建 Docker 镜像。

---

## 最终共识

本项目采用：

> **Go 1.25 + go-telegram/bot + 单进程模块化单体 + 每房间 Actor + 强类型 Command/Effect Reducer + 进程内 Outbox + SQLite/sqlc + 完整分层测试。**

整体原则是：

- 规则纯净；
- 房间有序；
- 消息可控；
- 数据够用；
- 低资源、易部署；
- 不为尚未确定的扩展功能提前引入分布式复杂度。

---

## Sources

[1] https://api.github.com/repos/go-telegram/bot — go-telegram/bot repository metadata

[2] https://github.com/go-telegram/bot/releases/tag/v1.23.0 — go-telegram/bot v1.23.0

[3] https://api.github.com/repos/mymmrac/telego — mymmrac/telego repository metadata

[4] https://raw.githubusercontent.com/mymmrac/telego/main/README.md — Telego README
