"""Minimal T2V API server wrapping LTX-2.3 (22B distilled) for benchmark smoke testing.

Synchronous generation — returns video metadata when complete.
Usage: source /disk/ssd1/ltx23-env/bin/activate
       python server.py --model-dir /disk/ssd1/models --port 8006

Requires the LTX-2.3 environment: ltx-pipelines, ltx-core, transformers<5.0
RDNA3-specific: CPU text encoder, torch.no_grad instead of inference_mode
"""
import argparse
import gc
import json
import os
import sys
import time
import uuid
from contextlib import contextmanager
from http.server import HTTPServer, BaseHTTPRequestHandler

import torch

_pipeline = None
_args = None

# RDNA3 workarounds
os.environ.setdefault("HSA_OVERRIDE_GFX_VERSION", "11.0.0")
os.environ.setdefault("PYTORCH_HIP_ALLOC_CONF", "max_split_size_mb:128")
os.environ.setdefault("TORCH_ROCM_AOTRITON_ENABLE_EXPERIMENTAL", "1")


def get_pipeline():
    global _pipeline
    if _pipeline is not None:
        return _pipeline

    model_dir = _args.model_dir

    # ── Monkey-patch: CPU text encoder for RDNA3 (hipBLASLt NaN bug) ──
    from ltx_pipelines.utils.blocks import PromptEncoder
    from ltx_pipelines.utils.gpu_model import gpu_model

    @contextmanager
    def cpu_text_encoder_ctx(self, streaming_prefetch_count=None):
        model = self._text_encoder_builder.build(
            device=torch.device("cpu"), dtype=torch.float32
        ).eval()
        try:
            yield model
        finally:
            model.to("meta")
            del model
            gc.collect()

    PromptEncoder._text_encoder_ctx = cpu_text_encoder_ctx

    def patched_pe_call(self, prompts, *, enhance_first_prompt=False,
                         enhance_prompt_image=None, enhance_prompt_seed=42,
                         streaming_prefetch_count=None):
        with self._text_encoder_ctx(streaming_prefetch_count) as text_encoder:
            raw_outputs = [text_encoder.encode(p) for p in prompts]
        gpu_outputs = []
        for hs_tuple, mask in raw_outputs:
            gpu_hs = tuple(h.to(device=self._device, dtype=self._dtype) for h in hs_tuple)
            gpu_mask = mask.to(device=self._device)
            gpu_outputs.append((gpu_hs, gpu_mask))
        del raw_outputs
        gc.collect()
        with gpu_model(
            self._embeddings_processor_builder.build(
                device=self._device, dtype=self._dtype
            ).to(self._device).eval()
        ) as proc:
            return [proc.process_hidden_states(hs, mask) for hs, mask in gpu_outputs]

    PromptEncoder.__call__ = patched_pe_call

    # ── Pipeline setup ──
    from ltx_pipelines.ti2vid_two_stages import TI2VidTwoStagesPipeline
    from ltx_core.loader import LTXV_LORA_COMFY_RENAMING_MAP, LoraPathStrengthAndSDOps

    checkpoint = os.path.join(model_dir, "ltx-2.3/ltx-2.3-22b-distilled.safetensors")
    distilled_lora = os.path.join(model_dir, "ltx-2.3/ltx-2.3-22b-distilled-lora-384.safetensors")
    spatial_up = os.path.join(model_dir, "ltx-2.3/ltx-2.3-spatial-upscaler-x2-1.1.safetensors")
    gemma_root = os.path.join(model_dir, "gemma-3-12b-it")

    print(f"Loading LTX-2.3 pipeline from {model_dir} ...")
    pipe = TI2VidTwoStagesPipeline(
        checkpoint_path=checkpoint,
        distilled_lora=[LoraPathStrengthAndSDOps(
            distilled_lora, 1.0, LTXV_LORA_COMFY_RENAMING_MAP
        )],
        spatial_upsampler_path=spatial_up,
        gemma_root=gemma_root,
        loras=[],
    )

    _pipeline = pipe
    print("LTX-2.3 pipeline loaded.")
    return pipe


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health" or self.path == "/":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"status": "ok"}).encode())
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length)) if length else {}

        prompt = body.get("prompt", "A beautiful sunset over the ocean")
        neg_prompt = body.get("negative_prompt", "blurry, low quality, jittery")
        width = body.get("width", 768)
        height = body.get("height", 512)
        num_frames = body.get("num_frames", 41)
        steps = body.get("num_inference_steps", 30)
        seed = body.get("seed", 42)
        fps = body.get("fps", 24)

        # Duration to frames: 4n+1
        duration = body.get("duration", 0)
        if duration > 0 and fps > 0:
            num_frames = max(int(duration * fps) // 4 * 4 + 1, 5)

        from ltx_core.components.guiders import MultiModalGuiderParams
        from ltx_core.model.video_vae.tiling import TilingConfig, SpatialTilingConfig, TemporalTilingConfig
        from ltx_core.model.video_vae import get_video_chunks_number
        from ltx_pipelines.utils.media_io import encode_video

        pipe = get_pipeline()

        tiling = TilingConfig(
            spatial_config=SpatialTilingConfig(tile_size_in_pixels=256, tile_overlap_in_pixels=32),
            temporal_config=TemporalTilingConfig(tile_size_in_frames=32, tile_overlap_in_frames=8),
        )
        video_guider = MultiModalGuiderParams(
            cfg_scale=3.0, stg_scale=1.0, rescale_scale=0.7,
            modality_scale=3.0, skip_step=0, stg_blocks=[28],
        )
        audio_guider = MultiModalGuiderParams(
            cfg_scale=7.0, stg_scale=1.0, rescale_scale=0.7,
            modality_scale=3.0, skip_step=0, stg_blocks=[28],
        )

        start = time.time()
        with torch.no_grad():
            video, audio = pipe(
                prompt=prompt,
                negative_prompt=neg_prompt,
                seed=seed,
                height=height,
                width=width,
                num_frames=num_frames,
                frame_rate=float(fps),
                num_inference_steps=steps,
                video_guider_params=video_guider,
                audio_guider_params=audio_guider,
                images=[],
                tiling_config=tiling,
                streaming_prefetch_count=None,
                max_batch_size=1,
            )
        elapsed = time.time() - start

        # Save video
        video_id = str(uuid.uuid4())[:8]
        out_path = f"/tmp/t2v_{video_id}.mp4"
        with torch.no_grad():
            encode_video(
                video=video, fps=float(fps), audio=audio,
                output_path=out_path,
                video_chunks_number=get_video_chunks_number(num_frames, tiling),
            )

        gc.collect()
        torch.cuda.empty_cache()

        resp = {
            "status": "completed",
            "video_url": out_path,
            "elapsed_seconds": round(elapsed, 2),
            "width": width,
            "height": height,
            "num_frames": num_frames,
            "fps": fps,
        }
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(resp).encode())

    def log_message(self, fmt, *args):
        print(f"[{time.strftime('%H:%M:%S')}] {fmt % args}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-dir", default="/disk/ssd1/models")
    parser.add_argument("--port", type=int, default=8006)
    args = parser.parse_args()
    _args = args

    # Pre-load pipeline
    get_pipeline()

    server = HTTPServer(("0.0.0.0", args.port), Handler)
    print(f"T2V server listening on port {args.port}")
    server.serve_forever()
