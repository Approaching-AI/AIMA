# Qwen3.6-35B-A3B DFlash 调优计划（GB10-4t）

## 目标

在 `gb10-4t (100.91.39.109)` 上，围绕 `Qwen3.6-35B-A3B + DFlash` 这条推理链路，针对以下两组负载继续追性能：

- `32K input + 1K output`
- `64K input + 1K output`

目标不是再证明 DFlash “能跑”，而是尽量把它在真实长上下文单路场景里的收益，明显拉高一截。

## 当前已知事实

### 1. 当前 DFlash 路径已经可用，但还不是最优形态

当前跑通的是一个 GB10 定制路径：

- 基底镜像：`qujing/vllm-gemma4-gb10:0.19.0-torchmoe2`
- DFlash 镜像：`qujing/vllm-gb10-dflash-fa2:latest`
- 路线特点：
  - 保留 GB10 base 已验证的 Torch/CUDA 栈
  - 通过 Python overlay 补 DFlash 相关逻辑
  - 单独注入 `_vllm_fa2_C.abi3.so`

这条路径的优势是能在 GB10 上稳定起服；劣势是它不是一条“原生、干净、官方默认”的 vLLM DFlash 路线，而是兼容性优先的工程解法。

### 2. 当前显式配置的关键变量并不多

当前 DFlash 主要显式设置了：

- `attention_backend=FLASH_ATTN`
- `max_model_len=262144`
- `max_num_batched_tokens=8192`
- `block_size=1280`
- `speculative_config.method=dflash`
- `speculative_config.num_speculative_tokens=15`
- `gpu_memory_utilization=0.92`
- `enforce_eager=true`
- runtime env:
  - `VLLM_KV_CACHE_LAYOUT=NHD`
  - `VLLM_SKIP_GDN_PREFILL_WARMUP=1`
  - `FLA_GDN_FIX_BT=1`

反过来说，当前这轮并没有把下面这些变量当成受控变量：

- `enable_chunked_prefill`
- `max_num_partial_prefills`
- `max_long_partial_prefills`
- `long_prefill_token_threshold`
- `max_num_seqs`
- `performance_mode`
- `optimization_level`

### 3. AIMA benchmark 当前测的是更偏“真实在线服务”的口径

AIMA 的 `benchmark run` 当前走的是 OpenAI 兼容的 `chat/completions` SSE 路径，不是自写 `/v1/completions` decode-heavy 脚本。

对这次调优来说，这一点很重要：

- 这组 benchmark 更接近真实服务链路
- 但它的 prompt 也是 synthetic 的长文本填充，不一定等价于真实业务 prompt
- 因此后续实验最好同时保留两类输入：
  - AIMA 默认 synthetic prompt
  - 代表性业务 prompt（通过 `--prompt` 注入）

### 4. 当前结果说明：DFlash 不是没用，而是没有把上限打出来

已有 `512 output` 长上下文结果显示：

- `32K` 左右：DFlash 相对 MTP 只是小幅领先
- `64K` 左右：DFlash 已经出现更明显的优势

这说明：

- DFlash 在这台机子上是有效的
- 但当前引擎参数和 runtime 形态，很可能没有把它推到更好的工作点

### 5. Drafter 本身也可能是限制因素

`z-lab/Qwen3.6-35B-A3B-DFlash` 的官方 model card 目前明确写着：

- `This Draft model is still under training (2000 steps).`

这意味着：

- 当前 Qwen3.6 的 drafter 本身还不是一个完全收敛的最终版本
- 即便引擎参数完全调对，它的 acceptance 上限也可能低于更成熟的 DFlash 模型

所以这条线需要分开看：

1. 引擎有没有把现有 drafter 的潜力用足
2. drafter 本身是否还存在模型上限

## 为什么目前只有大约 50% 左右的提升

当前最像真因的，不是单一点问题，而是几项因素叠加：

### A. 负载仍然有很重的 prefill 成分

`32K/64K input + 1K output` 并不是纯 decode-heavy。

这意味着：

- DFlash 的 decode 加速再强，也会被 prefill 时间稀释
- 如果要把端到端收益继续拉大，就不能只盯着 spec decode 本身
- 必须同时优化 prefill/scheduler/KV cache 路径

### B. `max_num_batched_tokens=8192` 很可能偏保守

这是当前最值得优先怀疑的变量之一。

vLLM 的官方优化文档明确指出：

- V1 里 chunked prefill 默认开启
- `max_num_batched_tokens` 是调 TTFT 和吞吐的核心旋钮
- 更高的值会改善 TTFT，尤其是长 prompt
- 对吞吐优化，建议至少高于 `8096`

而 DFlash 官方 vLLM quick start 给出的例子是：

- `--max-num-batched-tokens 32768`

相比之下，当前这条 GB10 路线只给了 `8192`。对 `32K` 和 `64K` prompt 来说，这很可能让 prefill 被切成过多轮次，直接把 TTFT 和端到端吞吐吃掉。

### C. 当前 `enforce_eager=true` 很可能在留性能

这条线当前所有 YAML 和启动参数都把 `enforce_eager` 打开了。

这么做的好处是稳定；坏处是：

- 禁掉了更激进的 graph / compile 路径
- 对 decode 和 steady-state 性能都可能有明显损失

如果后续能让 DFlash 路线在非 eager 模式下稳定，这有机会成为一项高收益实验。

### D. `block_size=1280` 是“跑通优先”的兼容性值，不一定是性能最优值

这个值不是从官方 quick start 直接来的，而是当前 GB10 兼容路径里逐步收敛出来的。

它的工程意义是：

- 先绕过 FA2 / paged-KV / hybrid cache 的兼容问题

但它不自动等于：

- 这是这台机器上的最佳性能点

因此 `block_size` 也值得后续单独做一次小范围 sweep，不过优先级应低于 `max_num_batched_tokens` 和 `num_speculative_tokens`。

### E. `num_speculative_tokens=15` 未必适合当前 workload

当前值来自 DFlash 的官方示例，但是否适合：

- `Qwen3.6`
- `GB10`
- `32K/64K long context`
- `1K output`

还没有被单独验证。

如果 acceptance length 只有 4-6 左右，那么：

- spec 太长会带来额外 draft/verify 浪费
- 反而未必比 `8/12` 这类更保守的值更优

### F. benchmark prompt 与官方 benchmark 数据集并不一致

官方 DFlash model card 的结果来自：

- 单张 `B200`
- `SGLang`
- thinking enabled
- `max output length = 4096`
- benchmark dataset 如 `GSM8K / Math500 / HumanEval / MBPP / MT-Bench`

而当前这条 AIMA benchmark：

- 是 `chat/completions`
- 主要是 synthetic prompt 填充
- `output=512`（接下来会测 `1K`）
- backend 是当前 GB10 定制 vLLM 路线

这两类结果本来就不该期待完全对齐。

## 调优时必须额外采集的指标

如果只看 `TTFT / TPOT / throughput`，调参效率会很低。后续每轮实验建议至少补下面这组指标。

### 1. AIMA benchmark 指标

- `avg_input_tokens`
- `avg_output_tokens`
- `ttft_p50_ms`
- `tpot_p50_ms`
- `throughput_tps`
- `qps`

### 2. vLLM `/metrics`

优先抓这些：

- `vllm:request_prefill_time_seconds`
- `vllm:request_decode_time_seconds`
- `vllm:time_to_first_token_seconds`
- `vllm:kv_cache_usage_perc`
- `vllm:num_preemptions`
- `vllm:spec_decode_num_accepted_tokens`
- `vllm:spec_decode_num_accepted_tokens_per_pos`
- `vllm:spec_decode_num_draft_tokens`
- `vllm:spec_decode_num_drafts`

用这组数据至少能回答四个关键问题：

1. DFlash 到底是在 prefill 端慢，还是在 decode 端没拉开
2. `num_speculative_tokens` 是不是过大或过小
3. acceptance 是不是在长上下文下塌得很厉害
4. KV cache / preemption 有没有在拖后腿

### 3. 日志统计

如果当前 vLLM build 支持 speculative decoding stats logging，建议打开。

重点关心：

- acceptance length
- accepted throughput
- drafted throughput
- per-position acceptance rate

这比只看一个总 throughput 更容易定位 `num_speculative_tokens` 是否设错。

## 优先级最高的实验矩阵

下面按“收益 / 成本比”排序。

### Phase 0：先把当前口径重跑成 1K output 基线

先不要急着大 sweep，先把真实目标负载钉死：

- `32K input + 1K output`
- `64K input + 1K output`
- `concurrency=1`
- `rounds=3`
- 同时跑：
  - baseline
  - MTP
  - DFlash

而且每组都至少保留两种 prompt：

- AIMA 默认 synthetic prompt
- 代表性业务 prompt（通过 `--prompt`）

如果两类 prompt 的 DFlash 提升差异很大，就说明 acceptance 和 prompt 分布强相关，后续调参不能只看 synthetic prompt。

### Phase 1：先 sweep `max_num_batched_tokens`

这是第一优先级。

推荐值：

- `8192`
- `16384`
- `32768`
- `65536`（若显存/稳定性允许）

理由：

- 当前值 `8192` 偏保守
- 官方 DFlash vLLM quick start 给的是 `32768`
- 这项最可能直接改善：
  - TTFT
  - prefill 吞吐
  - 端到端吞吐

预期：

- `32K` 档最可能直接受益
- `64K` 档也可能明显改善 TTFT

优先判断标准：

- `request_prefill_time_seconds` 是否明显下降
- `ttft_p50_ms` 是否同步下降
- `decode tps` 不应明显恶化

### Phase 2：在最佳 token budget 上 sweep `num_speculative_tokens`

推荐值：

- `8`
- `12`
- `15`
- `20`

理由：

- `15` 是官方默认示例，不等于当前 workload 最优
- 当前目标负载是 long-context + 1K output，不一定和官方 benchmark 的 acceptance 行为一致

判断方法：

- 看端到端吞吐
- 更关键的是看：
  - `spec_decode_num_accepted_tokens`
  - `spec_decode_num_draft_tokens`
  - per-position acceptance

经验判断：

- 如果 acceptance length 偏低，`15` 往往过长
- 如果 acceptance 在 64K 上仍然稳定，`15` 甚至 `20` 才有机会真正吃满收益

### Phase 3：再 sweep `gpu_memory_utilization`

推荐值：

- `0.92`
- `0.94`
- `0.96`

理由：

- 更高的 KV 预算可能允许更大的 `max_num_batched_tokens`
- 也可能降低 preemption/recompute 风险

但这一步要注意：

- GB10 是 unified memory 机器
- 激进抬高 `gpu_memory_utilization` 可能带来更隐蔽的不稳定，而不是直接 OOM

因此判断标准不能只看“能不能起”，还要看：

- 是否出现 preemption
- 是否出现明显 latency 抖动
- 是否出现 warmup 后首请求异常慢

### Phase 4：验证 `enforce_eager=false` 是否可行

这是高收益但高风险实验。

建议在前 3 个 Phase 收敛后再做。

实验方式：

- 只在最优的 `max_num_batched_tokens / num_speculative_tokens / gpu_memory_utilization` 组合上做
- 分别测：
  - DFlash + eager
  - DFlash + non-eager

预期收益：

- steady-state decode 有机会进一步抬高
- 端到端吞吐也可能继续改善

风险：

- 当前这条 GB10 DFlash 路线本来就是兼容性优先
- 一旦退出 eager，可能碰上 graph/compile 的新兼容问题

### Phase 5：小范围验证 `block_size`

只有在前几项都做完之后，才值得动它。

建议候选值：

- `1024`
- `1280`
- `1536`
- `2048`

这里只适合小范围试探，不适合一上来大 sweep。因为它更像一个“底层兼容路径上的性能细调项”，不是第一层主旋钮。

## 预计最可能有效的变量排序

按当前信息，建议优先顺序如下：

1. `max_num_batched_tokens`
2. `num_speculative_tokens`
3. `gpu_memory_utilization`
4. `enforce_eager`
5. `block_size`

如果只允许先做 3 组实验，就先做前 3 项。

## 建议的最小实验集合

如果要最快判断“这条线能不能再明显拉开”，建议先跑这组最小集合：

### DFlash，32K/64K，1K output

- 固定：
  - `concurrency=1`
  - `rounds=3`
  - 代表性 prompt 一组，synthetic prompt 一组

- 实验 1：`max_num_batched_tokens`
  - `8192`
  - `32768`

- 实验 2：`num_speculative_tokens`
  - `8`
  - `15`

- 实验 3：`gpu_memory_utilization`
  - `0.92`
  - `0.94`

这样总共只需要 2 × 2 × 2 = 8 组 DFlash 实验，就能先回答：

- 当前瓶颈到底更像 prefill 问题，还是 acceptance 问题
- DFlash 是不是只是因为 token budget 没给够
- 还是 drafter 本身在这组 workload 上就是没有更好的 acceptance

## 当前最重要的判断

基于现有信息，当前更像下面这个结论：

- `DFlash` 这条线的潜力还没有被完全榨出来
- 但真正限制它的，未必是单一的“DFlash 算法不行”
- 更可能是：
  - 当前 GB10 路线过于保守的 scheduler/prefill 参数
  - `enforce_eager` 留下的性能损失
  - 以及 Qwen3.6 drafter 本身还没完全收敛

因此下一步最值得做的，不是继续泛泛讨论“为什么没有 2x/3x”，而是先用上面的实验矩阵把三件事定量拆开：

1. prefill/scheduler 限制占了多少
2. spec token 长度是否设错
3. drafter acceptance 本身还有多少天花板
