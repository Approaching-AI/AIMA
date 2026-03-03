# AIMA 文档索引

> AI-Inference-Managed-by-AI

本文档按领域组织，为每个模块提供独立的导航入口。

## 快速导航

| 领域 | 文档 | 主要内容 |
|------|------|----------|
| 核心原则 | [ARCHITECTURE.md](../design/ARCHITECTURE.md) | 设计原则、架构全景 |
| Model | [model.md](model.md) | 模型扫描、导入、删除、元数据 |
| Engine | [engine.md](engine.md) | 引擎镜像、拉取、导入、Native 二进制 |
| Runtime | [runtime.md](runtime.md) | K3S Runtime、Docker Runtime、Native Runtime、Multi-Runtime 抽象 |
| Knowledge | [knowledge.md](knowledge.md) | 知识库、配置解析、Pod 生成 |
| HAL | [hal.md](hal.md) | 硬件检测、能力向量 |
| K3S | [k3s.md](k3s.md) | K8s 集成、Pod 管理、HAMi |
| Stack | [stack.md](stack.md) | 基础设施（K3S, HAMi） |
| MCP | [mcp.md](mcp.md) | MCP 服务器、工具定义 |
| CLI | [cli.md](cli.md) | 命令行接口 |
| Agent | [agent.md](agent.md) | Go Agent、ZeroClaw Sidecar |
| Web UI | — | 嵌入式 SPA 仪表盘 (:6188/ui/, Alpine.js, 科幻驾驶舱风格) |

## 文档约定

1. **按包引用**：代码注释中使用 `<!-- ref:filename -->` 引用具体章节
2. **保持同步**：修改代码时同步更新对应文档
3. **渐进式呈现**：从接口定义 → 数据结构 → 核心算法
4. **代码优先**：文档描述实际实现的接口，而非理想设计

## 核心原则

详见 [ARCHITECTURE.md](../design/ARCHITECTURE.md)：
- P1-P8：分层架构原则
- INV-1/INV-2：无代码分支
- Less Code：最小化代码，知识优先

---

*最后更新：2026-02-27*
