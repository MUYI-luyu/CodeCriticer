# CodeCritic

CodeCritic 是一个面向 Go 代码变更的证据驱动审查工具。它把 Tree-sitter 变更解析、SSA/CHA 程序分析、有限代码召回和 LLM Investigator 组合成一条可观测的 review workflow，并输出带代码证据的 findings。

## 当前定位

- 输入：一个 Go 仓库和 unified diff。
- 输出：风险发现、证据位置、验证结果以及可选的完整 Trace。
- Agent：单个 Investigator Agent，由模型生成 JSON 工具决策，Go 侧执行白名单工具并维护证据、预算和停止条件。
- 阶段：Plan、Investigator、Review 是 workflow 阶段，不是三个彼此协作的独立 Agent。
- 非目标：当前没有自动修复、代码执行验证、向量 RAG、长期记忆或 Web 服务。

## 执行链路

```text
review <diff>
  -> diff 解析与 Tree-sitter 符号标注
  -> 风险种子 / 调查假设
  -> LLM Plan（失败时使用确定性目标回退）
  -> go/packages + SSA/CHA 构建程序分析索引
  -> Investigator（最多 8 步）
       read_code / search_code / find_callers / run_static_rules / dataflow
  -> Evidence 合并、去重、预算与覆盖检查
  -> LLM Review
  -> finding 位置归一化、证据验证；证据不足时最多一次补查重审
  -> Trace
```

每次工具调用都必须通过 Go 侧白名单分派。工具结果被转换为 Evidence，Finding 只能引用已有证据；重复调用、无新增证据或达到预算时，调查会停止。

## 能力与边界

### 代码理解

- `read_code`：读取仓库内指定文件行段，单次最多约 200 行。
- `search_code`：优先使用 `rg` 搜索 Go 源码；没有 `rg` 时回退到 Go 扫描。搜索排除 `.git`、`logs`、历史参考和文档目录，并限制单次输出。
- `find_callers`：基于 Go 加载结果和调用图查找直接调用方。
- `dataflow`：对函数/方法执行有限的数据流分析；字段、结构体字段和局部变量应使用代码读取或搜索工具。
- `run_static_rules`：运行内置静态规则并将结果纳入证据。

### 程序分析边界

目标仓库需要能被 `go/packages` 加载。若依赖、模块或源码存在编译/加载错误，workflow 仍可继续，但调用图和 dataflow 会不可用，只能依赖读取、搜索和静态规则；该降级会记录在 Trace 错误中。

### Trace 与 replay

Trace JSON 包含请求、计划、工具参数、代码证据、LLM 调用统计和 findings，可能包含源码片段与 prompt，默认按 `0600` 保存。`replay` 当前只读取 Trace 并打印摘要，不会重新执行 workflow，也不保证确定性重演。

## 构建与部署

要求 Go 1.22 或更高版本，以及可访问的 OpenAI-compatible LLM 网关。

```bash
go build -o codecritic ./cmd/codecritic
```

配置环境变量：

```bash
export CodeCritic_API_KEY=<api-key>
export CodeCritic_URL=<openai-compatible-base-url>
# 兼容旧名称：DEEPSEEK_API_KEY、DEEPSEEK_BASE_URL
```

模型可通过 `--model`，或分别通过 `--plan-model`、`--review-model` 指定。

## 使用

审查一个 diff：

```bash
./codecritic review ./change.diff --repo /path/to/go-repo --trace logs/review.json
```

`--workflow` 仅为历史兼容参数，当前 review 已始终走 workflow；新脚本不需要传入。

运行离线评测：

```bash
./codecritic eval --dataset internal/eval/testdata/cases --concurrency 4 --trace-dir logs
```

查看一份 Trace 摘要：

```bash
./codecritic replay logs/review.json
```

## 验证

当前模块的验证命令：

```bash
go test ./internal/... ./cmd/...
go test -race ./internal/workflow ./internal/eval
go vet ./internal/... ./cmd/...
```

当前结果：上述测试、race 测试、vet 和 `go build ./cmd/codecritic` 均通过。仓库中的 `参考项目/`、`重构codecritic/`、`文档/` 和 `logs/` 是历史材料、运行产物或实验数据，不属于当前 Go 构建入口；因此不以 `go test ./...` 作为当前项目的验证口径。

## 已识别的风险与维护项

- graph 构建失败会静默降低工具能力，调用方应关注 Trace 中的 `graph:` 错误。
- `read_code` 目前先读取整个文件再截取行段，超大文件会产生额外内存开销，尚无单文件大小上限。
- `rg` 不可用时的 Go 扫描回退与 rg 的忽略规则不完全等价，复杂仓库应优先安装 `rg`。
- 评测是并发 worker 模式；取消后不再投递/回传新 case，但已经进入的单个 LLM 请求仍取决于其 context 支持。
- `internal/graph/impact.go`、`internal/diff/summary.go` 和 `recall.Store.Symbol` 主要由单元测试或旧接口保留，当前 workflow 不把它们作为主路径；删除前需同步评估外部调用者。

## 目录说明

- `cmd/codecritic`：CLI 入口（review、eval、replay）。
- `internal/workflow`：workflow、Agent 调查循环、工具和 Trace。
- `internal/diff`：diff 解析与变更符号标注。
- `internal/graph`：Go packages、SSA/CHA、调用图和有限 dataflow。
- `internal/recall`：源码搜索与调用方召回。
- `internal/review`：LLM 客户端、提示词、静态规则和 finding 验证。
- `internal/eval`：数据集、并发评测、指标、归因和 EvalTrace。
