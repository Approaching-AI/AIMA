"""Z-Image: OpenAI-compatible image generation server using diffusers.

Exposes all Z-Image hyperparameters through the OpenAI /v1/images/generations
API. Standard OpenAI fields (prompt, n, size, response_format) work as usual.
Z-Image specific fields (negative_prompt, guidance_scale, num_inference_steps,
seed, cfg_normalization) are passed as extra JSON fields.
"""
import base64, io, os, time, traceback
from typing import Optional

import torch
from diffusers import ZImagePipeline
from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field

app = FastAPI(title="Z-Image Server")
pipe = None
MODEL_PATH = os.environ.get("MODEL_PATH", "/models/Z-Image")

# Detect if this is a Turbo checkpoint (no CFG, fewer steps)
IS_TURBO = "turbo" in MODEL_PATH.lower() or os.environ.get("Z_IMAGE_TURBO", "") == "1"

DEFAULT_STEPS = 8 if IS_TURBO else 28
DEFAULT_GUIDANCE = 0.0 if IS_TURBO else 4.0


class ImageRequest(BaseModel):
    model: str = "z-image"
    prompt: str
    negative_prompt: Optional[str] = Field(None, description="Content to avoid in the image")
    n: int = Field(1, ge=1, le=4, description="Number of images to generate")
    size: str = Field("512x512", description="Image dimensions, e.g. 512x512, 1024x1024, 1280x720")
    response_format: str = Field("b64_json", description="b64_json or url")
    # Z-Image hyperparameters
    guidance_scale: Optional[float] = Field(None, ge=0.0, le=20.0, description="CFG scale. 3.0-5.0 for base, 0.0 for Turbo")
    num_inference_steps: Optional[int] = Field(None, ge=1, le=100, description="Denoising steps. 28-50 for base, 8 for Turbo")
    seed: Optional[int] = Field(None, description="Random seed for reproducibility")
    cfg_normalization: Optional[bool] = Field(None, description="Enable CFG normalization (base model only)")
    # OpenAI compat fields (accepted but mapped)
    quality: Optional[str] = Field(None, description="standard or hd — maps to steps (28/50)")
    style: Optional[str] = Field(None, description="vivid or natural — maps to guidance_scale")


def parse_size(size: str) -> tuple[int, int]:
    """Parse 'WxH' string, clamp to 512-2048 range."""
    try:
        parts = size.lower().split("x")
        w, h = int(parts[0]), int(parts[1])
    except (ValueError, IndexError):
        w, h = 512, 512
    w = max(64, min(2048, w))
    h = max(64, min(2048, h))
    return w, h


@app.on_event("startup")
def load_model():
    global pipe
    mode = "Turbo" if IS_TURBO else "Base"
    print(f"Loading Z-Image pipeline ({mode}) from {MODEL_PATH}...", flush=True)
    pipe = ZImagePipeline.from_pretrained(MODEL_PATH, torch_dtype=torch.bfloat16)
    pipe = pipe.to("cuda")
    print(f"Z-Image loaded and ready ({mode}, default steps={DEFAULT_STEPS}, guidance={DEFAULT_GUIDANCE})", flush=True)


@app.get("/health")
def health():
    return {
        "status": "ok",
        "model": "z-image",
        "variant": "turbo" if IS_TURBO else "base",
        "ready": pipe is not None,
        "default_steps": DEFAULT_STEPS,
        "default_guidance_scale": DEFAULT_GUIDANCE,
    }


@app.get("/v1/models")
def list_models():
    return {
        "object": "list",
        "data": [{"id": "z-image", "object": "model", "owned_by": "tongyi-mai"}],
    }


@app.post("/v1/images/generations")
def generate(req: ImageRequest):
    if pipe is None:
        raise HTTPException(503, "Model not loaded")

    w, h = parse_size(req.size)

    # Resolve inference steps
    steps = req.num_inference_steps
    if steps is None:
        if req.quality == "hd":
            steps = 50
        else:
            steps = DEFAULT_STEPS

    # Resolve guidance scale
    guidance = req.guidance_scale
    if guidance is None:
        if req.style == "natural":
            guidance = 3.0
        elif req.style == "vivid":
            guidance = 5.0
        else:
            guidance = DEFAULT_GUIDANCE

    # Build pipeline kwargs
    pipe_kwargs = {
        "prompt": req.prompt,
        "height": h,
        "width": w,
        "num_inference_steps": steps,
        "guidance_scale": guidance,
    }

    if req.negative_prompt and not IS_TURBO:
        pipe_kwargs["negative_prompt"] = req.negative_prompt

    if req.cfg_normalization is not None and not IS_TURBO:
        pipe_kwargs["cfg_normalization"] = req.cfg_normalization

    if req.seed is not None:
        pipe_kwargs["generator"] = torch.Generator("cuda").manual_seed(req.seed)

    results = []
    try:
        with torch.inference_mode():
            for i in range(req.n):
                # Increment seed per image for variety when generating multiple
                if req.seed is not None and i > 0:
                    pipe_kwargs["generator"] = torch.Generator("cuda").manual_seed(req.seed + i)
                image = pipe(**pipe_kwargs).images[0]
                buf = io.BytesIO()
                image.save(buf, format="PNG")
                b64 = base64.b64encode(buf.getvalue()).decode()
                results.append({"b64_json": b64})
    except Exception as e:
        traceback.print_exc()
        raise HTTPException(500, f"Generation failed: {e}")

    return JSONResponse({
        "created": int(time.time()),
        "data": results,
        "usage": {
            "steps": steps,
            "guidance_scale": guidance,
            "size": f"{w}x{h}",
            "seed": req.seed,
        },
    })


if __name__ == "__main__":
    import uvicorn

    port = int(os.environ.get("PORT", "8188"))
    uvicorn.run(app, host="0.0.0.0", port=port)
