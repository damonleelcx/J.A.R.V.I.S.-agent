# FORGE

**一个可持久化、长时运行的 AI 工程伙伴。**

FORGE 建立在一条核心原则之上：

> **长时运行的 Agent，不应该是一次长时运行的 LLM 调用。**

它是一套持久化工作流系统：模型反复被唤醒，从数据库重建自己的状态，执行**有边界**
的一小段工作，持久化结果，然后安全地在之后继续 —— 跨越崩溃、重启、发布和模型故障。

```
用户目标 → Planner → 持久任务库 → 队列/调度器 → Agent Worker
             ↑                                        ↓
             └────── 重规划 ←── 检查点 ←── 校验 ←──── 工具
```

`Agent = 模型 + Harness + 持久状态 + 工作流引擎 + 工具 + 调度器 + 恢复 + 评测 + 可观测性`

产品定义（语音优先协作、项目模型、风险分级、领域包、人类权威模型）见
[`docs/prd.md`](docs/prd.md)；架构见 [`docs/architecture.md`](docs/architecture.md)。

---

## 当前状态

FORGE 分阶段交付，每个阶段独立测试、独立推送。
**下表中任何一项，在 CI 对真实 Postgres 跑绿之前，都不声称完成。**

| 阶段 | 范围 | 状态 |
|------|------|------|
| 0 | 基础设施：配置、数据库、迁移、错误码注册表、事件注册表、结构化日志、ID、CI | ✅ 完成 |
| 1 | 身份：注册、登录、邮箱验证、密码重置、会话、HTTP 接口层 | ✅ 完成 |
| 2 | 持久化引擎：目标、任务 DAG、任务队列、租约、检查点、时间线 | ✅ 完成 |
| 3 | Agent 主循环：Planner/Executor/Verifier、上下文装配、预算、审批闸门、人格 | ✅ 完成 |
| 4 | 工具：能力注册表、沙箱、诚实声明的不可用连接器 | ✅ 完成 |
| 5 | 控制台：目标管理、执行时间线、审批界面 | ⏳ 进行中 |
| 6 | 可观测性、评测套件、恢复演练、发布 | ⏳ 计划中 |

---

## 快速开始

**依赖：** Go 1.26+、Docker（用于本地 Postgres）、`make`。

```bash
git clone https://github.com/damonleelcx/J.A.R.V.I.S.-agent.git
cd J.A.R.V.I.S.-agent

cp .env.example .env
# 填入两个必需的密钥：
#   FORGE_SESSION_SECRET   openssl rand -base64 48
#   FORGE_LLM_API_KEY      你的 DashScope（通义千问）API Key

make db-up          # 在 :55840 启动 Postgres
make migrate        # 应用 schema（幂等，可重复执行）
make health         # 确认连通性
make check          # 格式检查、vet、完整测试套件
```

`make help` 列出所有可用目标。

---

## 读代码前值得知道的几个设计决策

### 迁移链在每次启动时全量重跑

`db.Migrate` 在**每次启动**时应用**全部**迁移，而不是跳过账本里标记为"已应用"的那些。

**为什么。** 数据库里的真实 schema 才是真相源，账本只是记录、不是权威。
如果某条 DDL 其实回滚了、账本却写着"已应用"，这条真正需要跑的迁移就会被永久静默地
抑制掉 —— 不做人工干预根本救不回来。全量重跑还有一个额外好处：幂等性在每次启动时
都被真实执行一遍，所以一条不可重复执行的迁移会在**下一次有人重启时**就暴露，而不是
几个月后在故障恢复现场才炸。

代价是每次启动多几毫秒的空转 DDL。`TestMigrationsAreIdempotent` 会对真实 Postgres
连跑三遍并比对完整 schema 快照 —— 它同时能抓住"重跑报错"和更隐蔽的
"两次都成功但 schema 不一致"。

### 校验模型与执行模型来自不同厂商家族

PRD **SAF-03** 要求：高风险结论必须由**独立于生成路径**的方法来校验。
让一个模型给自己的输出打分，不叫独立。因此 `FORGE_LLM_VERIFIER_MODEL` 默认
与 `FORGE_LLM_EXECUTOR_MODEL` 属于不同厂商家族，当两者同族时 `config.Load` 会告警。
这是一条以配置形式表达的**安全控制**，不是成本优化。

### 错误码和事件名只有一个来源

每个对运维可见的失败都带有稳定的错误码、原因，以及**强制要求的修复建议**。
一个无法告诉读者"下一步做什么"的失败就是死胡同，而死胡同正是长时运行的 Agent
在凌晨三点把人晾在原地的方式。

两个注册表都由**解析本仓库源码**的围栏测试守护，而不是靠列举自己要检查的对象。
一个"遍历自己所检查内容"的围栏是真空的：删掉一条只会让循环少转一圈。

### 是七道限制，不是一道

Agent 会沿着七条彼此独立的轴失控：迭代次数、工具调用数、token、成本、墙钟时间、
任务深度、任务总数。只限制其中一条等于没限制。七条全部在 `EngineConfig` 里，
并在启动时校验。

---

## 目录结构

```
cmd/forgectl/            运维 CLI：migrate、health、config、version
internal/platform/       跨领域基础设施
  config/                环境变量加载与校验
  db/                    Postgres 连接池、事务、迁移执行器
  db/sql/                迁移链（编译进二进制）
  errs/                  中央错误码注册表
  logx/                  结构化日志与事件名注册表
  id/                    带前缀、可按时间排序的标识符
  clock/                 可注入的时间源
docs/                    PRD、架构、决策记录
```

迁移通过 `//go:embed` 编译进二进制，且只存在于**一个**目录。
一个需要依赖同级目录才能跑迁移的二进制，就是一个可能被部署成"起不来"状态的二进制。

---

## 在 Go workspace 内构建

如果你把本仓库 clone 到一个存在上层 `go.work` 的目录树里，直接 `go build ./...` 会失败：

```
directory ... is contained in a module that is not one of the workspace modules
```

`Makefile` 已经设置了 `GOWORK=off`，所以 `make` 目标在两种情况下都能正常工作。
临时命令请自行前缀：`GOWORK=off go test ./...`

---

## 许可证

见 [LICENSE](LICENSE)。
