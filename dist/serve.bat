@echo off
REM ===== AIMA launcher for AMD Strix Halo (Ryzen AI Max+ 395 / Radeon 8060S, Win11) =====
REM 1) AIMA_ENGINE_DIR is now OPTIONAL.
REM    - Leave it commented out: on first deploy AIMA auto-downloads the official
REM      ROCm/HIP llama.cpp (b9330, win-hip-radeon-x64) and runs on the iGPU. (#85)
REM    - Set it to your OWN ROCm/HIP llama.cpp build dir to use that exact binary
REM      instead (ANY version) -- AIMA launches whatever it finds there. (#86)
REM      Must be a ROCm/HIP build, NOT the CUDA build (CUDA won't start on no-NVIDIA).
REM set AIMA_ENGINE_DIR=D:\tools\llama-hip-radeon
REM 2) A stable API key for the UI / clients (kept in sync into OpenClaw automatically).
set AIMA_API_KEY=local
REM 3) Serve: UI+proxy on :6188, MCP on :9090, bound to all interfaces for LAN access.
REM    Points at the latest dated build; older builds in this folder are kept for rollback.
"%~dp0aima-windows-amd64-v0.5-dev-amd-strix-halo-20260615.exe" serve --addr 0.0.0.0:6188 --api-key local --mcp
