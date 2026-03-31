---
name: aima-tts
description: Text-to-speech using AIMA's current local TTS model. Generate audio files from text.
metadata: {"openclaw":{"emoji":"🔊","requires":{"bins":["curl"]},"always":true}}
---

# AIMA Text-to-Speech

Generate speech audio from text using the TTS model currently managed by AIMA/OpenClaw.

## When to use

- The user explicitly asks for a spoken or voice reply.
- The user asks you to say something aloud instead of only writing it.
- The user wants a short audio clip in Chinese or English.

## Required behavior

- Write a short spoken script first.
- Run `{baseDir}/scripts/speak.sh` with that script.
- After OpenClaw attaches the generated media, reply with exactly `NO_REPLY`.
- If TTS generation fails, give a brief text fallback that says the audio conversion failed.
- Keep the spoken script short and in the user's language unless they ask for something longer.

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
