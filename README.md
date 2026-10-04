# CodeCritic

CodeCritic 是一个面向 Go 代码变更的证据驱动审查工具。它把 Tree-sitter 变更解析、SSA/CHA 程序分析、有限代码召回和 LLM Investigator 组合成一条可观测的 review workflow，并输出带代码证据的 Claims。

## 当前定位

- 输入：生产 review 使用 Git base/head commit 创建固定 Review Snapshot；Eval 和测试保留专用的直接 diff 入口。
- 输出：风险发现、证据位置、验证结果以及可选的完整 Trace。
- Agent：单个原生 Function Calling 循环；模型选择调查工具或提交 Claim，Go 侧维护工具安全、Evidence、预算和准入边界。
- 非目标：当前没有自动修复、向量 RAG、长期记忆或 Web 服务。

## 执行链路

```text
review --base <SHA> --head <SHA>
  -> 创建固定的 head worktree，并由 base/head 生成 Diff
  -> diff 解析、完整 hunk 与 Tree-sitter 符号标注
  -> 大 Diff 显式 manifest；未放入首轮上下文的 hunk 由 read_diff 获取
  -> go/packages + SSA/CHA 构建程序分析索引
  -> Agent tool loop（最多 8 次成功调查）
       read_diff / read_code / search_code / inspect_symbol / inspect_concurrency
       run_static_rules / run_go_validation / submit_claims
  -> Evidence 合并、去重、预算与 provenance 检查
  -> Claim 准入；证据引用不完整时在同一会话继续调查
  -> FinalReport + Trace
```

每次工具调用都必须通过 Go 侧白名单分派。工具结果被转换为 Evidence，Claim 只能引用已有证据；重复调用、无新增证据或达到预算时，调查会停止。

## 能力与边界

### 代码理解

- `read_diff`：按 manifest 读取一个完整 hunk，保留增删、上下文、新旧行号和符号。
- `read_code`：按紧凑行段、焦点行上下文或精确声明读取代码，单次最多 200 行。
- `search_code`：字面量搜索 Go 源码并返回带行号的邻近代码；明确区分零结果与搜索失败。
- `inspect_symbol`：基于 Go 类型对象查询 definition/references；基于 CHA 查询 callers/callees，并标注精确静态边或过近似动态边。
- `inspect_concurrency`：返回函数内及有限跨函数的锁、channel、goroutine、defer 和 may/must-held lockset 事实；只提供事实和不确定性，不判定 Bug。
- `run_static_rules`：运行内置静态规则并将结果纳入证据。
- `run_go_validation`：运行有时限的 compile/test/race；test 和 race 只有显式 `--allow-exec` 时才暴露给 Agent。

### 程序分析边界

目标仓库需要能被 `go/packages` 加载。若依赖、模块或源码存在编译/加载错误，workflow 仍可继续，但语义 references 和调用层级不可用，只能依赖 Diff、读取、搜索和静态规则；该降级会记录在 Trace 错误中。

### Trace 与 replay

Trace JSON 包含请求、Snapshot 元数据、完整 assistant/tool 会话、调查步骤、代码证据、LLM 调用统计、Claims 和 Verdicts，可能包含源码片段与 prompt，默认按 `0600` 保存。`replay` 当前只读取 Trace 并打印摘要，不会重新执行 workflow，也不保证确定性重演。

## 构建与部署

要求 Go 1.23 或更高版本，以及可访问的 OpenAI-compatible LLM 网关。MCP Server 使用官方 Go SDK；普通 `review` 和 `eval` 仍需要模型网关，`mcp` 子命令不需要 API Key。

```bash
go build -o codecritic ./cmd/codecritic
```

配置环境变量：

```bash
export CodeCritic_API_KEY=<api-key>
export CodeCritic_URL=<openai-compatible-base-url>
# 兼容旧名称：DEEPSEEK_API_KEY、DEEPSEEK_BASE_URL
```

模型通过 `--model` 指定。

## 使用

推荐审查固定的 Git 版本：

```bash
./codecritic review --repo /path/to/go-repo --base <base-sha> --head <head-sha> --trace logs/review.json
```

### MCP Server

通过 stdio 向 Claude Code、Codex 等 MCP Host 暴露 CodeCritic 的只读 Go 分析工具：

```bash
./codecritic mcp --repo /path/to/go-repo --base <base-sha> --head <head-sha>
```

Server 只暴露 `read_diff`、`read_code`、`search_code`、`inspect_symbol` 和 `inspect_concurrency`。启动时创建固定的 Review Snapshot，并在 MCP 初始化信息中提供 Base/Head/DiffHash 与 hunk manifest；所有工具调用始终绑定这个 Snapshot。MCP 与内置 Agent 共用同一套严格参数解码、工具执行和 `Evidence`，不开放编译、测试或其他命令执行能力。

MCP Host 配置示例：

```json
{
  "mcpServers": {
    "codecritic": {
      "command": "/absolute/path/to/codecritic",
      "args": [
        "mcp",
        "--repo", "/absolute/path/to/go/repo",
        "--base", "<base-sha>",
        "--head", "<head-sha>"
      ]
    }
  }
}
```

运行离线评测：

```bash
./codecritic eval --dataset internal/eval/testdata/cases --concurrency 4 --trace-dir logs
```

Eval Trace 会分别记录 Outcome、Evidence、Investigation、Efficiency 和 Infra 指标，不生成综合分。可选语义 judge 独立评估 Claim 是否匹配真实问题，以及它引用的 Evidence 是否足够：

```bash
./codecritic eval --model gpt-5.5 --judge-model gpt-5.5 --trace-dir logs
```

确定性上下文投影只裁剪完全重复的工具结果和被更大窗口完整覆盖的旧 `read_code` 内容；完整会话仍保存在 Trace。可单独启用，也可成对运行 A/B：

```bash
./codecritic eval --model gpt-5.5 --context-projection --trace-dir logs/projected
./codecritic eval --model gpt-5.5 --ab-context --trace-dir logs/context-ab
```

A/B 两组保持配置一致，但模型采样和部分分析遍历没有固定随机种子，因此单次差异只能作为描述性结果；未发生裁剪的案例变化不能归因于 Context Projection。

对基线中已完成但漏报的案例，可以运行隔离的工具探针、Evidence 注入和完整上下文注入。该结果只提供方向性定位，不证明唯一根因，也不参与生产 Agent：

```bash
./codecritic eval --model gpt-5.5 --judge-model gpt-5.5 --diagnose --trace-dir logs/diagnosis
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
- `internal/graph`：Go packages、类型对象、SSA/CHA 和符号/调用层级查询。
- `internal/recall`：源码搜索与调用方召回。
- `internal/review`：LLM 客户端、提示词、静态规则和 finding 验证。
- `internal/eval`：数据集、并发评测、指标、归因和 EvalTrace。
