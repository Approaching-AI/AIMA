@echo off
REM ===== AIMA launcher for AMD Strix Halo (Ryzen AI Max+ 395 / Radeon 8060S, Win11) =====
REM 1) Point AIMA_ENGINE_DIR at a ROCm/HIP build of llama.cpp (NOT the CUDA build).
set AIMA_ENGINE_DIR=D:\tools\llama-hip-radeon
REM 2) A stable API key for the UI / clients (kept in sync into OpenClaw automatically).
set AIMA_API_KEY=local
REM 3) Serve: UI+proxy on :6188, MCP on :9090, bound to all interfaces for LAN access.
"%~dp0aima-windows-amd64.exe" serve --addr 0.0.0.0:6188 --api-key local --mcp
