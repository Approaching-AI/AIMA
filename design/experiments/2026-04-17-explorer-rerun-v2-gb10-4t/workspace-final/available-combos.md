# Available Combos

_Generated: 2026-04-17 07:27:08 · Agent: read-only · AIMA: regenerates each cycle_

This document is authoritative for new scheduling. Only rows under `## Ready Combos` may appear in new tasks.
This document is refreshed before each PDCA phase; plan.md snapshots may refer to an earlier state.

## Ready Combos

| Model | Engine | Runtime | Deploy Artifact | Reason |
|-------|--------|---------|-----------------|--------|
| GLM-4.5-Air-nvfp4 | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | pending: validate_long_context, tune |
| GLM-4.7-Flash-NVFP4 | sglang | docker | lmsysorg/sglang:dev-arm64-cu13 | pending: validate_baseline |
| MiniCPM-o-4_5 | sglang | docker | lmsysorg/sglang:dev-arm64-cu13 | pending: validate_baseline |

## Already Explored

| Model | Engine | Runtime | Deploy Artifact | Reason |
|-------|--------|---------|-----------------|--------|
| Qwen2.5-Omni-7B | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | recently failed |
| GLM-4.1V-9B-Thinking | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | recently cancelled |
| GLM-4.1V-9B-Thinking | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | recently cancelled |
| GLM-4.1V-9B-Thinking | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | recently cancelled |
| GLM-4.1V-9B-Thinking | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | recently cancelled |
| GLM-4.1V-9B-Thinking | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | recently cancelled |
| GLM-4.1V-9B-Thinking | z-image-diffusers | container | qujing-z-image:latest | recently cancelled |
| GLM-4.1V-9B-Thinking-FP4 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | recently completed |
| GLM-4.1V-9B-Thinking-FP4 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | recently completed |
| GLM-4.1V-9B-Thinking-FP4 | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | recently completed |
| GLM-4.1V-9B-Thinking-FP4 | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | recently completed |
| GLM-4.1V-9B-Thinking-FP4 | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | recently completed |
| GLM-4.1V-9B-Thinking-FP4 | z-image-diffusers | container | qujing-z-image:latest | recently completed |
| GLM-4.6V-Flash-FP4 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | recently completed |
| GLM-4.6V-Flash-FP4 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | recently completed |
| GLM-4.6V-Flash-FP4 | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | recently completed |
| GLM-4.6V-Flash-FP4 | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | recently completed |
| GLM-4.6V-Flash-FP4 | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | recently completed |
| GLM-4.6V-Flash-FP4 | z-image-diffusers | container | qujing-z-image:latest | recently completed |
| GLM-4.7-Flash-NVFP4 | vllm | docker | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | recently failed |
| GLM-4.7-Flash-NVFP4 | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | recently failed |
| Qwen2.5-Coder-3B-Instruct | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | recently failed |
| Qwen2.5-Coder-3B-Instruct | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | recently failed |
| Qwen2.5-Coder-3B-Instruct | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | recently failed |
| Qwen2.5-Coder-3B-Instruct | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | recently failed |
| Qwen2.5-Coder-3B-Instruct | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | recently failed |
| Qwen2.5-Coder-3B-Instruct | z-image-diffusers | container | qujing-z-image:latest | recently failed |
| Qwen2.5-Coder-7B-Instruct | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | recently cancelled |
| Qwen2.5-Coder-7B-Instruct | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | recently cancelled |
| Qwen2.5-Coder-7B-Instruct | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | recently cancelled |
| Qwen2.5-Coder-7B-Instruct | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | recently cancelled |
| Qwen2.5-Coder-7B-Instruct | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | recently cancelled |
| Qwen2.5-Coder-7B-Instruct | z-image-diffusers | container | qujing-z-image:latest | recently cancelled |
| Qwen3-ASR-1.7B | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | recently failed |
| Qwen3-ASR-1.7B | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | recently failed |
| Qwen3-ASR-1.7B | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | recently failed |
| Qwen3-ASR-1.7B | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | recently failed |
| Qwen3-ASR-1.7B | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | recently failed |
| Qwen3-ASR-1.7B | z-image-diffusers | container | qujing-z-image:latest | recently failed |
| Qwen3-Coder-Next-FP8 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | recently failed |
| Qwen3-Coder-Next-FP8 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | recently failed |
| Qwen3-Coder-Next-FP8 | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | recently failed |
| Qwen3-Coder-Next-FP8 | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | recently failed |
| Qwen3-Coder-Next-FP8 | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | recently failed |
| Qwen3-Coder-Next-FP8 | z-image-diffusers | container | qujing-z-image:latest | recently failed |
| Qwen3-TTS-0.6B | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | recently failed |
| Qwen3.5-27B | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | recently failed |
| Qwen3.5-35B-A3B | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | recently failed |
| Qwen3.5-35B-A3B | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | recently failed |
| Qwen3.5-35B-A3B | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | recently failed |
| Qwen3.5-35B-A3B | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | recently failed |
| Qwen3.5-35B-A3B | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | recently failed |
| Qwen3.5-35B-A3B | z-image-diffusers | container | qujing-z-image:latest | recently failed |
| Qwen3.5-9B | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | recently failed |
| Qwen3.5-9B | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | recently failed |
| Qwen3.5-9B | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | recently failed |
| Qwen3.5-9B | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | recently failed |
| Qwen3.5-9B | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | recently failed |
| Qwen3.5-9B | z-image-diffusers | container | qujing-z-image:latest | recently failed |
| Z-Image | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | recently failed |
| Z-Image | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | recently failed |
| Z-Image | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | recently failed |
| Z-Image | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | recently failed |
| Z-Image | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | recently failed |
| Z-Image | z-image-diffusers | docker | qujing-z-image:latest | recently failed |
| bge-reranker-v2-m3 | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | recently failed |
| MiniCPM-o-4_5 | vllm | docker | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | recently failed |
| MiniCPM-o-4_5 | vllm-nightly | docker | vllm/vllm-openai:qwen3_5-cu130 | recently failed |

## Blocked Combos

| Model | Engine | Runtime | Deploy Artifact | Reason |
|-------|--------|---------|-----------------|--------|
| Qwen2.5-Omni-7B | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| Qwen2.5-Omni-7B | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| Qwen2.5-Omni-7B | sglang | docker | lmsysorg/sglang:dev-arm64-cu13 | model Qwen2.5-Omni-7B not found locally and auto-pull is disabled |
| Qwen2.5-Omni-7B | vllm | docker | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | model Qwen2.5-Omni-7B not found locally and auto-pull is disabled |
| Qwen2.5-Omni-7B | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| FLUX.2-dev | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "image_gen" (supported: [asr]) |
| FLUX.2-dev | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "image_gen" (supported: [tts]) |
| FLUX.2-dev | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | modality mismatch: engine sglang does not support model type "image_gen" (supported: [llm vlm embedding]) |
| FLUX.2-dev | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | modality mismatch: engine vllm does not support model type "image_gen" (supported: [llm vlm embedding]) |
| FLUX.2-dev | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | modality mismatch: engine vllm-nightly does not support model type "image_gen" (supported: [llm vlm embedding]) |
| FLUX.2-dev | z-image-diffusers | container | qujing-z-image:latest | resolve auto-detected config for FLUX.2-dev: no variant of model "FLUX.2-dev" for engine "z-image-diffusers" gpu_arch "Blackwell" (vram 122570 MiB) |
| GLM-4.5-Air-FP8 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| GLM-4.5-Air-FP8 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| GLM-4.5-Air-FP8 | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | resolve auto-detected config for GLM-4.5-Air-FP8: no variant of model "GLM-4.5-Air-FP8" for engine "sglang" gpu_arch "Blackwell" (vram 122570 MiB) |
| GLM-4.5-Air-FP8 | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | resolve auto-detected config for GLM-4.5-Air-FP8: no variant of model "GLM-4.5-Air-FP8" for engine "vllm" gpu_arch "Blackwell" (vram 122570 MiB) |
| GLM-4.5-Air-FP8 | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | resolve auto-detected config for GLM-4.5-Air-FP8: no variant of model "GLM-4.5-Air-FP8" for engine "vllm-nightly" gpu_arch "Blackwell" (vram 122570 MiB) |
| GLM-4.5-Air-FP8 | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| GLM-4.5-Air-nvfp4 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| GLM-4.5-Air-nvfp4 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| GLM-4.5-Air-nvfp4 | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | resolve config: no variant of model "GLM-4.5-Air-nvfp4" for engine "sglang" gpu_arch "Blackwell" (vram 122570 MiB) |
| GLM-4.5-Air-nvfp4 | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | resolve config: no variant of model "GLM-4.5-Air-nvfp4" for engine "vllm" gpu_arch "Blackwell" (vram 122570 MiB) |
| GLM-4.5-Air-nvfp4 | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| GLM-4.7-Flash | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| GLM-4.7-Flash | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| GLM-4.7-Flash | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | resolve config: no variant of model "GLM-4.7-Flash" for engine "sglang" gpu_arch "Blackwell" (vram 122570 MiB) |
| GLM-4.7-Flash | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | resolve config: no variant of model "GLM-4.7-Flash" for engine "vllm" gpu_arch "Blackwell" (vram 122570 MiB) |
| GLM-4.7-Flash | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | resolve config: no variant of model "GLM-4.7-Flash" for engine "vllm-nightly" gpu_arch "Blackwell" (vram 122570 MiB) |
| GLM-4.7-Flash | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| GLM-4.7-Flash-NVFP4 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| GLM-4.7-Flash-NVFP4 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| GLM-4.7-Flash-NVFP4 | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| GLM-5-NVFP4 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| GLM-5-NVFP4 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| GLM-5-NVFP4 | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | resolve auto-detected config for GLM-5-NVFP4: no variant of model "GLM-5-NVFP4" for engine "sglang" gpu_arch "Blackwell" (vram 122570 MiB) |
| GLM-5-NVFP4 | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | resolve auto-detected config for GLM-5-NVFP4: no variant of model "GLM-5-NVFP4" for engine "vllm" gpu_arch "Blackwell" (vram 122570 MiB) |
| GLM-5-NVFP4 | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | resolve auto-detected config for GLM-5-NVFP4: no variant of model "GLM-5-NVFP4" for engine "vllm-nightly" gpu_arch "Blackwell" (vram 122570 MiB) |
| GLM-5-NVFP4 | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| MiniMax-M2.5 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| MiniMax-M2.5 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| MiniMax-M2.5 | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | resolve auto-detected config for MiniMax-M2.5: no variant of model "MiniMax-M2.5" for engine "sglang" gpu_arch "Blackwell" (vram 122570 MiB) |
| MiniMax-M2.5 | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | resolve auto-detected config for MiniMax-M2.5: no variant of model "MiniMax-M2.5" for engine "vllm" gpu_arch "Blackwell" (vram 122570 MiB) |
| MiniMax-M2.5 | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | resolve auto-detected config for MiniMax-M2.5: no variant of model "MiniMax-M2.5" for engine "vllm-nightly" gpu_arch "Blackwell" (vram 122570 MiB) |
| MiniMax-M2.5 | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| PDF-parser | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | model type unknown: engine glm-asr-fastapi requires one of [asr] |
| PDF-parser | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | model type unknown: engine qwen-tts-fastapi requires one of [tts] |
| PDF-parser | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | on-disk model format "onnx" incompatible with engine sglang (supported: [safetensors]) |
| PDF-parser | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | on-disk model format "onnx" incompatible with engine vllm (supported: [safetensors]) |
| PDF-parser | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | on-disk model format "onnx" incompatible with engine vllm-nightly (supported: [safetensors]) |
| PDF-parser | z-image-diffusers | container | qujing-z-image:latest | model type unknown: engine z-image-diffusers requires one of [image_gen] |
| qwen2.5-0.5b-instruct-q4_k_m | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| qwen2.5-0.5b-instruct-q4_k_m | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| qwen2.5-0.5b-instruct-q4_k_m | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | on-disk model format "gguf" incompatible with engine sglang (supported: [safetensors]) |
| qwen2.5-0.5b-instruct-q4_k_m | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | on-disk model format "gguf" incompatible with engine vllm (supported: [safetensors]) |
| qwen2.5-0.5b-instruct-q4_k_m | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | on-disk model format "gguf" incompatible with engine vllm-nightly (supported: [safetensors]) |
| qwen2.5-0.5b-instruct-q4_k_m | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| Qwen3-TTS-0.6B | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| Qwen3-TTS-0.6B | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | resolve config: no variant of model "Qwen3-TTS-0.6B" for engine "sglang" gpu_arch "Blackwell" (vram 122570 MiB) |
| Qwen3-TTS-0.6B | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | resolve config: no variant of model "Qwen3-TTS-0.6B" for engine "vllm" gpu_arch "Blackwell" (vram 122570 MiB) |
| Qwen3-TTS-0.6B | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | resolve config: no variant of model "Qwen3-TTS-0.6B" for engine "vllm-nightly" gpu_arch "Blackwell" (vram 122570 MiB) |
| Qwen3-TTS-0.6B | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| Qwen3.5-122B-A10B-FP8 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| Qwen3.5-122B-A10B-FP8 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| Qwen3.5-122B-A10B-FP8 | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | resolve auto-detected config for Qwen3.5-122B-A10B-FP8: no variant of model "Qwen3.5-122B-A10B-FP8" for engine "sglang" gpu_arch "Blackwell" (vram 122570 MiB) |
| Qwen3.5-122B-A10B-FP8 | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | resolve auto-detected config for Qwen3.5-122B-A10B-FP8: no variant of model "Qwen3.5-122B-A10B-FP8" for engine "vllm" gpu_arch "Blackwell" (vram 122570 MiB) |
| Qwen3.5-122B-A10B-FP8 | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | resolve auto-detected config for Qwen3.5-122B-A10B-FP8: no variant of model "Qwen3.5-122B-A10B-FP8" for engine "vllm-nightly" gpu_arch "Blackwell" (vram 122570 MiB) |
| Qwen3.5-122B-A10B-FP8 | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| Qwen3.5-27B | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| Qwen3.5-27B | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| Qwen3.5-27B | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | resolve config: no variant of model "Qwen3.5-27B" for engine "sglang" gpu_arch "Blackwell" (vram 122570 MiB) |
| Qwen3.5-27B | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | resolve config: no variant of model "Qwen3.5-27B" for engine "vllm" gpu_arch "Blackwell" (vram 122570 MiB) |
| Qwen3.5-27B | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| SenseVoiceSmall | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | model type unknown: engine glm-asr-fastapi requires one of [asr] |
| SenseVoiceSmall | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | model type unknown: engine qwen-tts-fastapi requires one of [tts] |
| SenseVoiceSmall | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | on-disk model format "pytorch" incompatible with engine sglang (supported: [safetensors]) |
| SenseVoiceSmall | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | on-disk model format "pytorch" incompatible with engine vllm (supported: [safetensors]) |
| SenseVoiceSmall | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | on-disk model format "pytorch" incompatible with engine vllm-nightly (supported: [safetensors]) |
| SenseVoiceSmall | z-image-diffusers | container | qujing-z-image:latest | model type unknown: engine z-image-diffusers requires one of [image_gen] |
| Step-3.5-Flash-FP8 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| Step-3.5-Flash-FP8 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| Step-3.5-Flash-FP8 | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | resolve auto-detected config for Step-3.5-Flash-FP8: no variant of model "Step-3.5-Flash-FP8" for engine "sglang" gpu_arch "Blackwell" (vram 122570 MiB) |
| Step-3.5-Flash-FP8 | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | resolve auto-detected config for Step-3.5-Flash-FP8: no variant of model "Step-3.5-Flash-FP8" for engine "vllm" gpu_arch "Blackwell" (vram 122570 MiB) |
| Step-3.5-Flash-FP8 | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | resolve auto-detected config for Step-3.5-Flash-FP8: no variant of model "Step-3.5-Flash-FP8" for engine "vllm-nightly" gpu_arch "Blackwell" (vram 122570 MiB) |
| Step-3.5-Flash-FP8 | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| VoxCPM2 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | model type unknown: engine glm-asr-fastapi requires one of [asr] |
| VoxCPM2 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | model type unknown: engine qwen-tts-fastapi requires one of [tts] |
| VoxCPM2 | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | model type unknown: engine sglang requires one of [llm vlm embedding] |
| VoxCPM2 | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | model type unknown: engine vllm requires one of [llm vlm embedding] |
| VoxCPM2 | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | model type unknown: engine vllm-nightly requires one of [llm vlm embedding] |
| VoxCPM2 | z-image-diffusers | container | qujing-z-image:latest | model type unknown: engine z-image-diffusers requires one of [image_gen] |
| onnx | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "embedding" (supported: [asr]) |
| onnx | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "embedding" (supported: [tts]) |
| onnx | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | on-disk model format "onnx" incompatible with engine sglang (supported: [safetensors]) |
| onnx | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | on-disk model format "onnx" incompatible with engine vllm (supported: [safetensors]) |
| onnx | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | on-disk model format "onnx" incompatible with engine vllm-nightly (supported: [safetensors]) |
| onnx | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "embedding" (supported: [image_gen]) |
| bge-reranker-v2-m3 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "reranker" (supported: [asr]) |
| bge-reranker-v2-m3 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "reranker" (supported: [tts]) |
| bge-reranker-v2-m3 | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | modality mismatch: engine sglang does not support model type "reranker" (supported: [llm vlm embedding]) |
| bge-reranker-v2-m3 | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | modality mismatch: engine vllm does not support model type "reranker" (supported: [llm vlm embedding]) |
| bge-reranker-v2-m3 | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "reranker" (supported: [image_gen]) |
| gemma-4-26B-A4B-it | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| gemma-4-26B-A4B-it | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| gemma-4-26B-A4B-it | sglang | container | lmsysorg/sglang:dev-arm64-cu13 | resolve config: no variant of model "gemma-4-26B-A4B-it" for engine "sglang" gpu_arch "Blackwell" (vram 122570 MiB) |
| gemma-4-26B-A4B-it | vllm | container | qujing/vllm-gemma4-gb10:0.19.0-torchmoe2 | resolve config: no variant of model "gemma-4-26B-A4B-it" for engine "vllm" gpu_arch "Blackwell" (vram 122570 MiB) |
| gemma-4-26B-A4B-it | vllm-nightly | container | vllm/vllm-openai:qwen3_5-cu130 | resolve config: model "gemma-4-26b-a4b-it" with engine "vllm-nightly" is marked unsupported: vllm-nightly currently points to the qwen3_5-cu130 image and is not a validated Gemma 4 runtime; use vllm-gemma4-blackwell or another explicitly cataloged Gemma path. |
| gemma-4-26B-A4B-it | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |
| MiniCPM-o-4_5 | glm-asr-fastapi | container | qujing-glm-asr-nano:latest | modality mismatch: engine glm-asr-fastapi does not support model type "llm" (supported: [asr]) |
| MiniCPM-o-4_5 | qwen-tts-fastapi | container | qujing-qwen3-tts-real:latest | modality mismatch: engine qwen-tts-fastapi does not support model type "llm" (supported: [tts]) |
| MiniCPM-o-4_5 | z-image-diffusers | container | qujing-z-image:latest | modality mismatch: engine z-image-diffusers does not support model type "llm" (supported: [image_gen]) |

