---
name: aima-tts
description: Text-to-speech using AIMA's current local TTS model. Generate audio files from text.
metadata: {"openclaw":{"emoji":"🔊","requires":{"bins":["curl"]},"always":true}}
---

# AIMA Text-to-Speech

Generate speech audio from text using the TTS model currently managed by AIMA/OpenClaw.

## Quick start

```bash
{baseDir}/scripts/speak.sh "你好世界" --filename hello.wav
```

## Useful flags

```bash
{baseDir}/scripts/speak.sh "今天天气真好" --filename weather.wav
{baseDir}/scripts/speak.sh "Hello AIMA" --filename greeting.wav --voice default
```

## Output

- WAV audio file saved to workspace
- `MEDIA:` line printed for OpenClaw auto-attachment

## Notes

- Model: auto-detected from `~/.openclaw/openclaw.json` (override with `AIMA_TTS_MODEL`)
- Voice: `default` (single voice)
- Output format: WAV
- Runs on AIMA proxy at `http://127.0.0.1:6188/v1`
