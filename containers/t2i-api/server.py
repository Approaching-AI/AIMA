"""Minimal T2I API server wrapping FLUX.2-dev for benchmark smoke testing.

Exposes OpenAI-compatible /v1/images/generations endpoint.
Supports NF4 quantization (--nf4) for single-GPU deployment on 48GB cards.
Usage: python server.py --model-path /models/FLUX.2-dev --port 8000 [--nf4] [--gpu 2]

Requires: diffusers, transformers, torch, bitsandbytes (for --nf4)
"""
import argparse
import base64
import io
import json
import os
import time
from http.server import HTTPServer, BaseHTTPRequestHandler

import torch

_pipe = None
_args = None

# RDNA3 workaround
os.environ.setdefault("HSA_OVERRIDE_GFX_VERSION", "11.0.0")


def get_pipeline():
    global _pipe
    if _pipe is not None:
        return _pipe

    model_path = _args.model_path
    print(f"Loading FLUX.2 pipeline from {model_path} ...")

    if _args.nf4:
        from diffusers import Flux2Pipeline, Flux2Transformer2DModel
        from diffusers import BitsAndBytesConfig as DiffusersBnBConfig
        from transformers import BitsAndBytesConfig as TransformersBnBConfig, AutoModel

        diff_quant = DiffusersBnBConfig(
            load_in_4bit=True, bnb_4bit_quant_type="nf4",
            bnb_4bit_compute_dtype=torch.bfloat16,
        )
        tf_quant = TransformersBnBConfig(
            load_in_4bit=True, bnb_4bit_quant_type="nf4",
            bnb_4bit_compute_dtype=torch.bfloat16,
        )

        print("Loading transformer (NF4)...")
        transformer = Flux2Transformer2DModel.from_pretrained(
            model_path, subfolder="transformer",
            quantization_config=diff_quant, torch_dtype=torch.bfloat16,
        )
        print("Loading text encoder (NF4)...")
        text_encoder = AutoModel.from_pretrained(
            model_path, subfolder="text_encoder",
            quantization_config=tf_quant, torch_dtype=torch.bfloat16,
        )
        print("Building pipeline...")
        pipe = Flux2Pipeline.from_pretrained(
            model_path, transformer=transformer, text_encoder=text_encoder,
            torch_dtype=torch.bfloat16, local_files_only=True,
        )
        pipe.enable_model_cpu_offload(gpu_id=0)
    else:
        from diffusers import Flux2Pipeline

        pipe = Flux2Pipeline.from_pretrained(
            model_path, torch_dtype=torch.bfloat16, local_files_only=True,
        )
        try:
            pipe.enable_model_cpu_offload()
        except ImportError:
            pipe.to("cuda")

    _pipe = pipe
    print("Pipeline loaded.")
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
        if self.path != "/v1/images/generations":
            self.send_response(404)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"detail": "Not Found"}).encode())
            return

        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length)) if length else {}

        prompt = body.get("prompt", "A beautiful landscape")
        size = body.get("size", "512x512")
        n = body.get("n", 1)
        steps = body.get("num_inference_steps", 20)

        # Parse size
        try:
            w, h = size.split("x")
            width, height = int(w), int(h)
        except Exception:
            width, height = 512, 512

        pipe = get_pipeline()

        images_b64 = []
        for _ in range(n):
            image = pipe(
                prompt=prompt,
                width=width,
                height=height,
                num_inference_steps=steps,
                guidance_scale=3.5,
                generator=torch.Generator(device="cpu").manual_seed(int(time.time()) % 2**32),
            ).images[0]

            buf = io.BytesIO()
            image.save(buf, format="PNG")
            images_b64.append({
                "b64_json": base64.b64encode(buf.getvalue()).decode(),
            })

        resp = {
            "created": int(time.time()),
            "data": images_b64,
        }
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(resp).encode())

    def log_message(self, fmt, *args):
        print(f"[{time.strftime('%H:%M:%S')}] {fmt % args}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-path", default="/models/FLUX.2-dev")
    parser.add_argument("--port", type=int, default=8000)
    parser.add_argument("--gpu", type=int, default=0)
    parser.add_argument("--nf4", action="store_true", help="Use NF4 quantization (fits single 48GB GPU)")
    args = parser.parse_args()
    _args = args

    os.environ["HIP_VISIBLE_DEVICES"] = str(args.gpu)

    # Pre-load pipeline
    get_pipeline()

    server = HTTPServer(("0.0.0.0", args.port), Handler)
    print(f"T2I server listening on port {args.port}")
    server.serve_forever()
