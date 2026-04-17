# Device Profile

_Generated: 2026-04-17 11:12:43 · Agent: read-only · AIMA: regenerates each cycle_

## Hardware

| Field | Value |
|-------|-------|
| Profile | nvidia-gb10-arm64 |
| GPU Arch | Blackwell |
| GPU Count | 1 |
| VRAM per GPU (MiB) | 122570 |
| Total VRAM (MiB) | 122570 |

## Local Models

| Name | Format | Type | Size (GiB) | Max Context | Fits VRAM |
|------|--------|------|------------|-------------|----------|
| Qwen2.5-Omni-7B | safetensors | llm | 1.77 | — | ✅ |
| FLUX.2-dev | safetensors | image_gen | 165.39 | — | ❌ (VRAM overflow) |
| GLM-4.1V-9B-Thinking | safetensors | llm | 19.17 | 8K | ✅ |
| GLM-4.1V-9B-Thinking-FP4 | safetensors | llm | 8.25 | 8K | ✅ |
| GLM-4.5-Air-FP8 | safetensors | llm | 104.83 | — | ❌ (VRAM overflow) |
| GLM-4.5-Air-nvfp4 | safetensors | llm | 57.68 | 16K | ✅ |
| GLM-4.6V-Flash-FP4 | safetensors | llm | 8.25 | 8K | ✅ |
| GLM-4.7-Flash | safetensors | llm | 58.16 | 64K | ✅ |
| GLM-4.7-Flash-NVFP4 | safetensors | llm | 19.04 | — | ✅ |
| GLM-5-NVFP4 | safetensors | llm | 410.66 | — | ❌ (VRAM overflow) |
| MiniMax-M2.5 | safetensors | llm | 214.33 | — | ❌ (VRAM overflow) |
| PDF-parser | onnx |  | 0.25 | — | ✅ |
| qwen2.5-0.5b-instruct-q4_k_m | gguf | llm | 0.46 | — | ✅ |
| Qwen2.5-Coder-3B-Instruct | safetensors | llm | 5.75 | 8K | ✅ |
| Qwen2.5-Coder-7B-Instruct | safetensors | llm | 14.19 | 8K | ✅ |
| Qwen3-ASR-1.7B | safetensors | llm | 4.38 | 8K | ✅ |
| Qwen3-Coder-Next-FP8 | safetensors | llm | 74.86 | 256K | ✅ |
| Qwen3-TTS-0.6B | safetensors | llm | 1.70 | — | ✅ |
| Qwen3.5-122B-A10B-FP8 | safetensors | llm | 118.43 | — | ❌ (VRAM overflow) |
| Qwen3.5-27B | safetensors | llm | 51.75 | 64K | ✅ |
| Qwen3.5-35B-A3B | safetensors | llm | 66.97 | 256K | ✅ |
| Qwen3.5-9B | safetensors | llm | 17.98 | 64K | ✅ |
| SenseVoiceSmall | pytorch |  | 0.87 | — | ✅ |
| Step-3.5-Flash-FP8 | safetensors | llm | 194.24 | — | ❌ (VRAM overflow) |
| VoxCPM2 | safetensors |  | 4.27 | — | ✅ |
| Z-Image | safetensors | image_gen | 19.11 | — | ✅ |
| onnx | onnx | embedding | 0.00 | — | ✅ |
| bge-reranker-v2-m3 | safetensors | reranker | 2.12 | — | ✅ |
| gemma-4-26B-A4B-it | safetensors | llm | 48.07 | 152K | ✅ |
| MiniCPM-o-4_5 | safetensors | llm | 17.46 | — | ✅ |

## Local Engines

| Type | Runtime | Deploy Artifact | Features | Tunable Params |
|------|---------|-----------------|----------|----------------|
| glm-asr-fastapi | container | qujing-glm-asr-nano:latest | cpu_inference, openai_compatible_asr | port |
| qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | cpu_inference, openai_compatible_tts | port |
| sglang | container | lmsysorg/sglang:dev-arm64-cu13 | flash_infer, radix_attention | port, mem_fraction_static |
| vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | flash_attention | gpu_memory_utilization, max_model_len, port, served_model_name |
| vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | triton_attention | port, served_model_name, gpu_memory_utilization, max_model_len, enable_chunked_prefill |
| z-image-diffusers | container | qujing-z-image:latest | gpu_inference, openai_compatible_image_gen, diffusion_model | port |

## Active Deployments (Live Snapshot)

_This table is a live `deploy list` snapshot taken just before the current phase. If a deploy appears here while this cycle's experiments were running, its GPU/VRAM/port resources were still held during those experiments — consider handoff effects when diagnosing failures._

_None_

