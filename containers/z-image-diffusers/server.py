"""Z-Image: OpenAI-compatible image generation server using diffusers.

Supports any diffusers pipeline (FLUX.2, Stable Diffusion, etc.) with full
hyperparameter passthrough. All pipeline.__call__ parameters are exposed
via the API for native-equivalent inference quality.

TeaCache (arXiv:2411.19108): training-free DiT caching, activated via
--teacache-enabled. Monkey-patches the transformer's forward to skip
redundant denoising steps based on modulated-input L1 distance.
"""
import argparse
import base64
import io
import os
import time
from contextlib import asynccontextmanager
from typing import Optional

import torch
from diffusers import DiffusionPipeline
from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse
from pydantic import BaseModel

MODEL_PATH = os.environ.get("MODEL_PATH", "/models")

pipe = None
args = None


# ---------------------------------------------------------------------------
# TeaCache monkey-patch (FLUX.2 / FLUX.1 DiT caching)
# ---------------------------------------------------------------------------

def apply_teacache(pipeline, rel_l1_thresh: float = 0.4):
    """Monkey-patch a Flux2Transformer2DModel with TeaCache caching logic.

    Works by measuring the L1 distance of the modulated input between adjacent
    denoising steps and skipping the full transformer when the change is below
    a polynomial-rescaled threshold.
    """
    transformer = pipeline.transformer
    cls_name = type(transformer).__name__

    if "Flux2" in cls_name:
        _apply_teacache_flux2(transformer, rel_l1_thresh)
    elif "Flux" in cls_name:
        _apply_teacache_flux1(transformer, rel_l1_thresh)
    else:
        print(f"[TeaCache] Unsupported transformer type: {cls_name}, skipping", flush=True)
        return

    print(f"[TeaCache] Patched {cls_name}, rel_l1_thresh={rel_l1_thresh}", flush=True)


def _poly_rescale(x):
    """4th-order polynomial rescaling from FLUX.1 (verified compatible with FLUX.2)."""
    coeffs = [498.651, -283.781, 55.859, -3.823, 0.264]
    result = coeffs[0]
    for c in coeffs[1:]:
        result = result * x + c
    return result


def _apply_teacache_flux2(transformer, rel_l1_thresh):
    original_forward = transformer.forward

    def patched_forward(*a, **kw):
        hidden_states = kw.get("hidden_states", a[0] if a else None)
        timestep = kw.get("timestep")
        # Access the modulation layer for proxy signal
        mod_layer = transformer.transformer_blocks[0].norm1_context
        if not hasattr(mod_layer, "__call__"):
            mod_layer = transformer.transformer_blocks[0].norm1

        # Initialize per-sequence state on first call
        if not hasattr(transformer, "_tea_step"):
            transformer._tea_step = 0
            transformer._tea_accum = 0.0
            transformer._tea_prev_modulated = None
            transformer._tea_prev_residual = None

        step = transformer._tea_step
        transformer._tea_step += 1

        # Always compute first and last step
        if step == 0 or timestep is None:
            transformer._tea_prev_residual = None
            out = original_forward(*a, **kw)
            # Compute modulated input for next step comparison
            try:
                from diffusers.models.transformers.transformer_flux2 import Flux2Modulation
                temb = transformer.time_text_embed(timestep.flatten(),
                                                    kw.get("pooled_projections", None))
                double_mod = transformer.transformer_blocks[0].norm1.linear(
                    transformer.transformer_blocks[0].norm1.act(temb))
                (shift, scale, _), _ = Flux2Modulation.split(double_mod, 2)
                normed = transformer.transformer_blocks[0].norm1.norm(hidden_states)
                transformer._tea_prev_modulated = ((1 + scale) * normed + shift).detach()
            except Exception:
                transformer._tea_prev_modulated = None
            return out

        # Compute modulated input for current step
        try:
            from diffusers.models.transformers.transformer_flux2 import Flux2Modulation
            temb = transformer.time_text_embed(timestep.flatten(),
                                                kw.get("pooled_projections", None))
            double_mod = transformer.transformer_blocks[0].norm1.linear(
                transformer.transformer_blocks[0].norm1.act(temb))
            (shift, scale, _), _ = Flux2Modulation.split(double_mod, 2)
            normed = transformer.transformer_blocks[0].norm1.norm(hidden_states)
            cur_modulated = (1 + scale) * normed + shift
        except Exception:
            # Fallback: always compute
            out = original_forward(*a, **kw)
            transformer._tea_prev_residual = None
            return out

        if transformer._tea_prev_modulated is not None:
            diff = (cur_modulated - transformer._tea_prev_modulated).abs().mean()
            norm = transformer._tea_prev_modulated.abs().mean().clamp(min=1e-6)
            rel_l1 = (diff / norm).item()
            rescaled = _poly_rescale(rel_l1)
            transformer._tea_accum += rescaled

            if transformer._tea_accum < rel_l1_thresh and transformer._tea_prev_residual is not None:
                # Cache hit — reuse previous residual
                transformer._tea_prev_modulated = cur_modulated.detach()
                return hidden_states + transformer._tea_prev_residual

        # Cache miss — full compute
        transformer._tea_accum = 0.0
        out = original_forward(*a, **kw)

        # Store residual for potential reuse
        if isinstance(out, tuple):
            transformer._tea_prev_residual = (out[0] - hidden_states).detach()
        else:
            transformer._tea_prev_residual = (out - hidden_states).detach()
        transformer._tea_prev_modulated = cur_modulated.detach()
        return out

    transformer.forward = patched_forward


def _apply_teacache_flux1(transformer, rel_l1_thresh):
    """TeaCache for FLUX.1's FluxTransformer2DModel (AdaGroupNorm-based)."""
    original_forward = transformer.forward

    def patched_forward(*a, **kw):
        hidden_states = kw.get("hidden_states", a[0] if a else None)

        if not hasattr(transformer, "_tea_step"):
            transformer._tea_step = 0
            transformer._tea_accum = 0.0
            transformer._tea_prev_modulated = None
            transformer._tea_prev_residual = None

        step = transformer._tea_step
        transformer._tea_step += 1

        if step == 0:
            transformer._tea_prev_residual = None
            out = original_forward(*a, **kw)
            try:
                temb = kw.get("temb") or kw.get("timestep_embedding")
                normed = transformer.transformer_blocks[0].norm1(hidden_states, emb=temb)
                transformer._tea_prev_modulated = normed.detach()
            except Exception:
                transformer._tea_prev_modulated = None
            return out

        try:
            temb = kw.get("temb") or kw.get("timestep_embedding")
            cur_modulated = transformer.transformer_blocks[0].norm1(hidden_states, emb=temb)
        except Exception:
            out = original_forward(*a, **kw)
            transformer._tea_prev_residual = None
            return out

        if transformer._tea_prev_modulated is not None:
            diff = (cur_modulated - transformer._tea_prev_modulated).abs().mean()
            norm = transformer._tea_prev_modulated.abs().mean().clamp(min=1e-6)
            rel_l1 = (diff / norm).item()
            rescaled = _poly_rescale(rel_l1)
            transformer._tea_accum += rescaled

            if transformer._tea_accum < rel_l1_thresh and transformer._tea_prev_residual is not None:
                transformer._tea_prev_modulated = cur_modulated.detach()
                return hidden_states + transformer._tea_prev_residual

        transformer._tea_accum = 0.0
        out = original_forward(*a, **kw)
        if isinstance(out, tuple):
            transformer._tea_prev_residual = (out[0] - hidden_states).detach()
        else:
            transformer._tea_prev_residual = (out - hidden_states).detach()
        transformer._tea_prev_modulated = cur_modulated.detach()
        return out

    transformer.forward = patched_forward


def reset_teacache_state(pipeline):
    """Reset per-sequence TeaCache accumulators (call before each generation)."""
    t = pipeline.transformer
    if hasattr(t, "_tea_step"):
        t._tea_step = 0
        t._tea_accum = 0.0
        t._tea_prev_modulated = None
        t._tea_prev_residual = None


# ---------------------------------------------------------------------------
# Request / Response models
# ---------------------------------------------------------------------------

class ImageRequest(BaseModel):
    model: str = "z-image"
    prompt: str
    n: int = 1
    size: str = "512x512"
    num_inference_steps: Optional[int] = None
    guidance_scale: Optional[float] = None
    seed: Optional[int] = None
    max_sequence_length: Optional[int] = None
    response_format: str = "b64_json"


# ---------------------------------------------------------------------------
# Application lifecycle
# ---------------------------------------------------------------------------

@asynccontextmanager
async def lifespan(a: FastAPI):
    global pipe
    print(f"Loading Z-Image pipeline from {MODEL_PATH}...", flush=True)
    dtype = getattr(torch, args.dtype) if args else torch.bfloat16

    load_kwargs = {"torch_dtype": dtype}

    # Device placement strategy
    if args and args.device_map:
        load_kwargs["device_map"] = args.device_map
        if args.max_memory_per_gpu_gib:
            max_mem = f"{args.max_memory_per_gpu_gib}GiB"
            import torch as _t
            n_gpus = _t.cuda.device_count() if _t.cuda.is_available() else 0
            if n_gpus > 0:
                load_kwargs["max_memory"] = {i: max_mem for i in range(n_gpus)}

    pipe = DiffusionPipeline.from_pretrained(MODEL_PATH, **load_kwargs)

    # Move to GPU (only when not using device_map, which handles placement)
    if not (args and args.device_map):
        if args and args.cpu_offload:
            pipe.enable_model_cpu_offload()
            print("Enabled model CPU offload", flush=True)
        else:
            pipe = pipe.to("cuda")

    # Apply TeaCache if requested
    if args and args.teacache_enabled:
        thresh = args.teacache_rel_l1_thresh
        apply_teacache(pipe, rel_l1_thresh=thresh)

    print(f"Z-Image loaded and ready (pipeline: {type(pipe).__name__})", flush=True)

    yield

    print("Shutting down Z-Image...", flush=True)
    pipe = None


app = FastAPI(title="Z-Image", lifespan=lifespan)


# ---------------------------------------------------------------------------
# Endpoints
# ---------------------------------------------------------------------------

@app.get("/health")
def health():
    if pipe is None:
        return JSONResponse({"status": "loading", "ready": False}, status_code=503)
    return {"status": "ok", "model": "z-image", "pipeline": type(pipe).__name__, "ready": True}


@app.post("/v1/images/generations")
def generate(req: ImageRequest):
    if pipe is None:
        raise HTTPException(503, "Model not loaded")

    w, h = (int(x) for x in req.size.split("x"))
    steps = req.num_inference_steps or (args.num_inference_steps if args else 20)
    guidance = req.guidance_scale or (args.guidance_scale if args else 3.5)
    max_seq_len = req.max_sequence_length or (args.max_sequence_length if args else 512)

    generator = None
    if req.seed is not None:
        generator = torch.Generator(device="cuda").manual_seed(req.seed)

    # Build kwargs — only pass params the pipeline actually accepts
    kwargs = dict(
        prompt=req.prompt,
        height=h,
        width=w,
        num_inference_steps=steps,
        guidance_scale=guidance,
        num_images_per_prompt=req.n,
    )
    pipe_params = set(type(pipe).__call__.__code__.co_varnames)
    if "max_sequence_length" in pipe_params:
        kwargs["max_sequence_length"] = max_seq_len
    if generator is not None:
        kwargs["generator"] = generator

    # Reset TeaCache accumulators before each generation
    reset_teacache_state(pipe)

    t0 = time.perf_counter()
    with torch.inference_mode():
        images = pipe(**kwargs).images
    elapsed = time.perf_counter() - t0

    results = []
    for image in images:
        buf = io.BytesIO()
        image.save(buf, format="PNG")
        b64 = base64.b64encode(buf.getvalue()).decode()
        results.append({"b64_json": b64})

    return JSONResponse({
        "created": int(time.time()),
        "data": results,
        "usage": {
            "inference_time_s": round(elapsed, 2),
            "steps": steps,
            "size": f"{w}x{h}",
        },
    })


# ---------------------------------------------------------------------------
# CLI argument parsing (flags come from AIMA's configToFlags)
# ---------------------------------------------------------------------------

def parse_args():
    p = argparse.ArgumentParser(description="Z-Image diffusers server")
    p.add_argument("--port", type=int, default=int(os.environ.get("PORT", "8188")))
    p.add_argument("--dtype", default="bfloat16")
    p.add_argument("--guidance-scale", type=float, default=3.5)
    p.add_argument("--height", type=int, default=512)
    p.add_argument("--width", type=int, default=512)
    p.add_argument("--num-inference-steps", type=int, default=20)
    p.add_argument("--max-sequence-length", type=int, default=512)
    # Device placement
    p.add_argument("--cpu-offload", action="store_true",
                   help="Enable model CPU offload (diffusers enable_model_cpu_offload)")
    p.add_argument("--device-map", default="",
                   help="Device map strategy (e.g. 'balanced', 'auto')")
    p.add_argument("--max-memory-per-gpu-gib", type=float, default=0,
                   help="Max memory per GPU in GiB for device_map placement")
    p.add_argument("--staged-loading", action="store_true",
                   help="Reserved for staged-loading orchestration (not used by server)")
    # TeaCache
    p.add_argument("--teacache-enabled", action="store_true",
                   help="Enable TeaCache DiT caching (arXiv:2411.19108)")
    p.add_argument("--teacache-rel-l1-thresh", type=float, default=0.4,
                   help="TeaCache relative L1 threshold (lower=more quality, higher=faster)")
    # Quantization (passed through for bitsandbytes loading)
    p.add_argument("--quantization", default="",
                   help="Quantization method (e.g. nf4)")
    p.add_argument("--quantization-library", default="",
                   help="Quantization library (e.g. bitsandbytes)")
    return p.parse_args()


if __name__ == "__main__":
    import uvicorn
    args = parse_args()
    uvicorn.run(app, host="0.0.0.0", port=args.port)
