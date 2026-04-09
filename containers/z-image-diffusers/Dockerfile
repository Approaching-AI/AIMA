ARG BASE_IMAGE=lmsysorg/sglang:dev-arm64-cu13
FROM ${BASE_IMAGE}

RUN pip install --no-cache-dir diffusers==0.37.1 accelerate fastapi uvicorn pydantic

WORKDIR /app
COPY server.py /app/server.py

EXPOSE 8188
ENTRYPOINT ["python3", "server.py"]
