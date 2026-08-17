# CodeCriticer

一个 Go 代码审查 agent：解析 unified diff，结合静态规则与 LLM 找出真实 bug，并通过调用图影响分析与反思剔除误报。

## 进度

- [x] 第 1 天：项目骨架 + 核心数据模型（`Finding`）+ unified diff 解析
- [ ] 静态规则引擎（x/tools analyzers）
- [ ] LLM 审查（Plan-and-Execute）
- [ ] 符号定位与调用图影响分析
- [ ] 代码召回与 Reflection 复核
- [ ] 评测数据集与 Recall/Precision 指标
