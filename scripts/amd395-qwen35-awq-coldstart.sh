#!/bin/bash
# ============================================================
# amd395 冷启动脚本: Qwen3.5-35B-A3B AWQ-4bit on vLLM ROCm
# ============================================================
# 设备: AMD Ryzen AI MAX+ 395 + Radeon 8060S (RDNA 3.5, gfx1151)
# 内存: 62GB 统一内存 (CPU/GPU 共享)
# 镜像: docker.1ms.run/kyuz0/vllm-therock-gfx1151:latest
# 模型: cyankiwi/Qwen3.5-35B-A3B-AWQ-4bit (~24GB safetensors)
# ============================================================

set -euo pipefail

# ── 配置 ──
CONTAINER_NAME="qwen35-a3b-awq"
MODEL_REPO="cyankiwi/Qwen3.5-35B-A3B-AWQ-4bit"
MODEL_DIR="/home/quings/data/models/Qwen3.5-35B-A3B-AWQ-4bit"
IMAGE="docker.1ms.run/kyuz0/vllm-therock-gfx1151:latest"
PORT=8000

# vLLM 参数
GPU_MEM_UTIL=0.90       # 统一内存 APU，可以给高一些
MAX_MODEL_LEN=32768     # 32K context, AWQ 4-bit 内存足够
DTYPE="half"            # ROCm AWQ 推荐 fp16

# ── Step 1: 下载模型 (如果不存在) ──
if [ ! -d "$MODEL_DIR" ] || [ -z "$(ls -A "$MODEL_DIR" 2>/dev/null)" ]; then
    echo ">>> [1/3] 下载模型: $MODEL_REPO → $MODEL_DIR"
    echo "    预计大小: ~24GB, 请耐心等待..."
    mkdir -p "$MODEL_DIR"

    # 优先用 huggingface-cli, 支持断点续传
    if command -v huggingface-cli &>/dev/null; then
        huggingface-cli download "$MODEL_REPO" \
            --local-dir "$MODEL_DIR" \
            --local-dir-use-symlinks False
    else
        pip install -q huggingface_hub
        python3 -c "
from huggingface_hub import snapshot_download
snapshot_download('$MODEL_REPO', local_dir='$MODEL_DIR', local_dir_use_symlinks=False)
"
    fi
    echo ">>> 模型下载完成"
else
    echo ">>> [1/3] 模型已存在: $MODEL_DIR, 跳过下载"
fi

# ── Step 2: 清理旧容器 ──
echo ">>> [2/3] 准备容器环境"
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "    清理旧容器: $CONTAINER_NAME"
    docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
fi

# ── Step 3: 启动 vLLM 容器 ──
echo ">>> [3/3] 启动 vLLM 推理服务"
echo "    镜像: $IMAGE"
echo "    模型: $MODEL_DIR"
echo "    端口: $PORT"
echo "    上下文: $MAX_MODEL_LEN tokens"

docker run -d \
    --name "$CONTAINER_NAME" \
    --device /dev/kfd \
    --device /dev/dri \
    --group-add video \
    --shm-size 16g \
    --security-opt seccomp=unconfined \
    -e HSA_OVERRIDE_GFX_VERSION=11.5.1 \
    -e VLLM_USE_TRITON_FLASH_ATTN=1 \
    -v "$MODEL_DIR":/model \
    -p "${PORT}:8000" \
    "$IMAGE" \
    vllm serve /model \
        --port 8000 \
        --dtype "$DTYPE" \
        --gpu-memory-utilization "$GPU_MEM_UTIL" \
        --max-model-len "$MAX_MODEL_LEN" \
        --quantization awq \
        --trust-remote-code \
        --served-model-name "qwen3.5-35b-a3b"

echo ""
echo "============================================"
echo "  容器已启动: $CONTAINER_NAME"
echo "  查看日志: docker logs -f $CONTAINER_NAME"
echo "  健康检查: curl http://localhost:$PORT/health"
echo "  API 端点: http://localhost:$PORT/v1"
echo "============================================"
echo ""
echo "等待模型加载 (可能需要 2-5 分钟)..."
echo "测试命令:"
echo '  curl http://localhost:8000/v1/chat/completions \'
echo '    -H "Content-Type: application/json" \'
echo '    -d '"'"'{"model":"qwen3.5-35b-a3b","messages":[{"role":"user","content":"你好"}],"max_tokens":64}'"'"''
