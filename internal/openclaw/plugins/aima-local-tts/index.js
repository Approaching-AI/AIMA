import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const defaultWorkspaceDir = path.join(os.homedir(), ".openclaw", "workspace");
const defaultLocalAPIKey = "local";
const allowedAudioExtensions = new Set([
  ".aac",
  ".flac",
  ".m4a",
  ".mp3",
  ".mp4",
  ".oga",
  ".ogg",
  ".opus",
  ".wav",
  ".webm",
]);
const allowedResponseFormats = new Set(["wav", "mp3", "opus", "flac", "aac", "pcm"]);

function asString(value) {
  return typeof value === "string" ? value.trim() : "";
}

function asOptionalBoolean(value, fallback = false) {
  if (typeof value === "boolean") return value;
  if (typeof value === "string") {
    const lowered = value.trim().toLowerCase();
    if (["1", "true", "yes", "on"].includes(lowered)) return true;
    if (["0", "false", "no", "off"].includes(lowered)) return false;
  }
  return fallback;
}

function asOptionalNumber(value) {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value.trim()) {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return undefined;
}

function normalizeHomePath(raw) {
  const value = asString(raw);
  if (!value) return "";
  if (value === "~") return os.homedir();
  if (value.startsWith("~/") || value.startsWith("~\\")) {
    return path.join(os.homedir(), value.slice(2));
  }
  if (value.startsWith("~")) return path.join(os.homedir(), value.slice(1));
  return value;
}

function normalizeBaseUrl(raw) {
  return asString(raw).replace(/\/+$/, "");
}

function toTextResult(text, details) {
  return {
    content: [{ type: "text", text }],
    details,
  };
}

function workspaceRoots(ctx) {
  const roots = [];
  const workspaceDir = asString(ctx?.workspaceDir);
  if (workspaceDir) roots.push(path.resolve(normalizeHomePath(workspaceDir)));
  roots.push(path.resolve(defaultWorkspaceDir));
  return [...new Set(roots)];
}

function resolveCandidatePath(rawPath, ctx) {
  const value = normalizeHomePath(rawPath);
  if (!value) return "";
  if (path.isAbsolute(value)) return path.resolve(value);
  for (const root of workspaceRoots(ctx)) {
    const candidate = path.resolve(root, value);
    if (candidate.startsWith(root + path.sep) || candidate === root) {
      return candidate;
    }
  }
  return "";
}

function isPathWithinRoots(filePath, roots) {
  for (const root of roots) {
    if (filePath === root || filePath.startsWith(root + path.sep)) return true;
  }
  return false;
}

function validateInputAudioPath(resolvedPath, roots) {
  if (!resolvedPath) return "path required";
  if (!isPathWithinRoots(resolvedPath, roots)) {
    return `path must stay within the OpenClaw workspace: ${roots[0]}`;
  }
  if (!fs.existsSync(resolvedPath)) return `audio file not found: ${resolvedPath}`;
  if (!fs.statSync(resolvedPath).isFile()) return `audio path is not a file: ${resolvedPath}`;
  const ext = path.extname(resolvedPath).toLowerCase();
  if (ext && !allowedAudioExtensions.has(ext)) {
    return `unsupported audio extension: ${ext}`;
  }
  return "";
}

function resolveOutputPath(rawPath, ctx, responseFormat) {
  const roots = workspaceRoots(ctx);
  if (!rawPath) {
    const stamp = new Date().toISOString().replace(/[:.]/g, "-");
    const ext = responseFormat === "pcm" ? "pcm" : responseFormat;
    return path.join(roots[0], "audio", `${stamp}-speech.${ext}`);
  }
  return resolveCandidatePath(rawPath, ctx);
}

function validateOutputPath(resolvedPath, roots) {
  if (!resolvedPath) return "output path required";
  if (!isPathWithinRoots(resolvedPath, roots)) {
    return `output path must stay within the OpenClaw workspace: ${roots[0]}`;
  }
  return "";
}

function normalizeAudioConfigEntry(entry) {
  if (!entry || typeof entry !== "object") return null;
  const model = asString(entry.model);
  if (!model) return null;
  return {
    model,
    baseUrl: normalizeBaseUrl(entry.baseUrl),
    apiKey: asString(entry.apiKey),
  };
}

function resolveConfiguredAudioRoutes(cfg) {
  const providers = cfg?.models?.providers && typeof cfg.models.providers === "object"
    ? cfg.models.providers
    : {};
  const aimaMediaProvider = providers["aima-media"];
  const aimaProvider = providers.aima;
  const fallbackBaseUrl = normalizeBaseUrl(
    aimaMediaProvider?.baseUrl || aimaProvider?.baseUrl,
  );
  const fallbackAPIKey =
    asString(aimaMediaProvider?.apiKey) ||
    asString(aimaProvider?.apiKey) ||
    defaultLocalAPIKey;

  const explicit = Array.isArray(cfg?.tools?.media?.audio?.models)
    ? cfg.tools.media.audio.models
        .map((entry) => normalizeAudioConfigEntry(entry))
        .filter(Boolean)
        .map((entry) => ({
          model: entry.model,
          baseUrl: entry.baseUrl || fallbackBaseUrl,
          apiKey: entry.apiKey || fallbackAPIKey,
        }))
        .filter((entry) => entry.baseUrl)
    : [];
  if (explicit.length > 0) return explicit;

  const providerModels = Array.isArray(aimaMediaProvider?.models) ? aimaMediaProvider.models : [];
  return providerModels
    .filter((entry) => {
      if (!entry || typeof entry !== "object") return false;
      const id = asString(entry.id);
      if (!id) return false;
      const inputs = Array.isArray(entry.input) ? entry.input.map(asString).filter(Boolean) : [];
      return inputs.length === 1 && inputs[0] === "text";
    })
    .map((entry) => ({
      model: asString(entry.id),
      baseUrl: fallbackBaseUrl,
      apiKey: fallbackAPIKey,
    }))
    .filter((entry) => entry.baseUrl);
}

function detectAudioMime(filePath) {
  switch (path.extname(filePath).toLowerCase()) {
    case ".aac":
      return "audio/aac";
    case ".flac":
      return "audio/flac";
    case ".m4a":
      return "audio/mp4";
    case ".mp3":
      return "audio/mpeg";
    case ".mp4":
      return "audio/mp4";
    case ".oga":
    case ".ogg":
      return "audio/ogg";
    case ".opus":
      return "audio/opus";
    case ".wav":
      return "audio/wav";
    case ".webm":
      return "audio/webm";
    default:
      return "application/octet-stream";
  }
}

function encodeAudioFileAsDataURL(filePath) {
  const mime = detectAudioMime(filePath);
  const encoded = fs.readFileSync(filePath).toString("base64");
  return `data:${mime};base64,${encoded}`;
}

function formatHTTPError(status, statusText, bodyText) {
  const prefix = statusText ? `${status} ${statusText}` : `${status}`;
  const trimmed = bodyText.trim();
  if (!trimmed) return prefix;
  return `${prefix}: ${trimmed.slice(0, 500)}`;
}

async function transcribeWithAIMA(cfg, filePath) {
  const routes = resolveConfiguredAudioRoutes(cfg);
  if (routes.length === 0) {
    throw new Error("no configured AIMA/OpenClaw audio transcription route");
  }

  const fileName = path.basename(filePath);
  const mime = detectAudioMime(filePath);
  const bytes = fs.readFileSync(filePath);
  const blob = new Blob([bytes], { type: mime });
  let lastError = "";

  for (const route of routes) {
    const url = `${route.baseUrl}/audio/transcriptions`;
    const form = new FormData();
    form.append("file", blob, fileName);
    form.append("model", route.model);

    const headers = new Headers();
    headers.set("authorization", `Bearer ${route.apiKey || defaultLocalAPIKey}`);

    const response = await fetch(url, {
      method: "POST",
      headers,
      body: form,
    });
    const bodyText = await response.text();
    if (!response.ok) {
      lastError = formatHTTPError(response.status, response.statusText, bodyText);
      continue;
    }

    let payload;
    try {
      payload = JSON.parse(bodyText);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      lastError = `invalid JSON transcription response: ${message}`;
      continue;
    }

    const transcript = asString(payload?.text);
    if (!transcript) {
      lastError = `missing text in transcription response from ${url}`;
      continue;
    }
    return {
      transcript,
      model: route.model,
      baseUrl: route.baseUrl,
    };
  }

  throw new Error(lastError || "audio transcription returned no transcript");
}

function resolveConfiguredTTSRoute(cfg) {
  const tts = cfg?.messages?.tts && typeof cfg.messages.tts === "object"
    ? cfg.messages.tts
    : {};
  const provider = tts?.providers?.openai && typeof tts.providers.openai === "object"
    ? tts.providers.openai
    : tts?.openai && typeof tts.openai === "object"
      ? tts.openai
      : null;
  if (!provider) return null;
  const model = asString(provider.model);
  const baseUrl = normalizeBaseUrl(provider.baseUrl);
  if (!model || !baseUrl) return null;
  return {
    model,
    baseUrl,
    apiKey: asString(provider.apiKey) || defaultLocalAPIKey,
  };
}

async function synthesizeWithAIMA(route, payload) {
  const headers = new Headers();
  headers.set("content-type", "application/json");
  headers.set("authorization", `Bearer ${route.apiKey || defaultLocalAPIKey}`);

  const response = await fetch(`${route.baseUrl}/tts`, {
    method: "POST",
    headers,
    body: JSON.stringify(payload),
  });
  const bodyText = await response.text();
  if (!response.ok) {
    throw new Error(formatHTTPError(response.status, response.statusText, bodyText));
  }

  let result;
  try {
    result = JSON.parse(bodyText);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    throw new Error(`invalid JSON TTS response: ${message}`);
  }

  const audioBase64 = asString(result?.audio_base64);
  if (!audioBase64) {
    throw new Error("missing audio_base64 in TTS response");
  }
  return result;
}

function readStringParam(record, keys) {
  for (const key of keys) {
    const value = asString(record[key]);
    if (value) return value;
  }
  return "";
}

function buildTTSRequest(route, params, referenceAudioValue, referenceText) {
  const record = params && typeof params === "object" ? params : {};
  const responseFormat = asString(record.responseFormat || record.response_format || "wav").toLowerCase() || "wav";
  if (!allowedResponseFormats.has(responseFormat)) {
    throw new Error(`unsupported response format: ${responseFormat}`);
  }
  const mode = asString(record.mode);
  const explicitVoice = readStringParam(record, ["voice"]);
  const wantsClone = mode === "voice_clone" || asOptionalBoolean(record.xVectorOnlyMode ?? record.x_vector_only_mode, false);
  if (wantsClone && !referenceAudioValue) {
    throw new Error("voice cloning requires referenceAudioPath");
  }

  const payload = {
    model: route.model,
    text: readStringParam(record, ["text", "input"]),
    response_format: responseFormat,
  };
  if (explicitVoice) payload.voice = explicitVoice;

  for (const [source, target] of [
    ["mode", "mode"],
    ["speaker", "speaker"],
    ["language", "language"],
    ["instruct", "instruct"],
  ]) {
    const value = asString(record[source]);
    if (value) payload[target] = value;
  }

  for (const [source, target] of [
    ["xVectorOnlyMode", "x_vector_only_mode"],
    ["x_vector_only_mode", "x_vector_only_mode"],
    ["nonStreamingMode", "non_streaming_mode"],
    ["non_streaming_mode", "non_streaming_mode"],
    ["doSample", "do_sample"],
    ["do_sample", "do_sample"],
    ["subtalkerDoSample", "subtalker_dosample"],
    ["subtalker_dosample", "subtalker_dosample"],
  ]) {
    if (record[source] !== undefined) {
      payload[target] = asOptionalBoolean(record[source]);
    }
  }

  for (const [source, target] of [
    ["speed", "speed"],
    ["topK", "top_k"],
    ["top_k", "top_k"],
    ["topP", "top_p"],
    ["top_p", "top_p"],
    ["temperature", "temperature"],
    ["repetitionPenalty", "repetition_penalty"],
    ["repetition_penalty", "repetition_penalty"],
    ["subtalkerTopK", "subtalker_top_k"],
    ["subtalker_top_k", "subtalker_top_k"],
    ["subtalkerTopP", "subtalker_top_p"],
    ["subtalker_top_p", "subtalker_top_p"],
    ["subtalkerTemperature", "subtalker_temperature"],
    ["subtalker_temperature", "subtalker_temperature"],
    ["maxNewTokens", "max_new_tokens"],
    ["max_new_tokens", "max_new_tokens"],
  ]) {
    if (record[source] === undefined) continue;
    const value = asOptionalNumber(record[source]);
    if (value !== undefined) payload[target] = value;
  }

  if (referenceAudioValue) payload.reference_audio = referenceAudioValue;
  if (referenceText) payload.reference_text = referenceText;
  return payload;
}

export default function register(api) {
  api.registerTool((ctx) => ({
    name: "audio_synthesize",
    label: "Audio Synthesize",
    description:
      "Generate speech audio with the synced local AIMA TTS model. Supports standard TTS plus reference-audio voice cloning from workspace files.",
    parameters: {
      type: "object",
      additionalProperties: false,
      properties: {
        text: {
          type: "string",
          description: "Text to synthesize.",
        },
        outputPath: {
          type: "string",
          description: "Optional workspace-relative or absolute output path under the OpenClaw workspace.",
        },
        responseFormat: {
          type: "string",
          enum: ["wav", "mp3", "opus", "flac", "aac", "pcm"],
          description: "Output audio format.",
        },
        voice: {
          type: "string",
          description: "Optional voice alias passed to the TTS backend.",
        },
        referenceAudioPath: {
          type: "string",
          description: "Optional workspace-relative or absolute path to a reference audio clip for voice cloning.",
        },
        referenceText: {
          type: "string",
          description: "Optional transcript for the reference audio clip. If omitted, the tool will try local ASR first and otherwise fall back to x-vector-only cloning.",
        },
        xVectorOnlyMode: {
          type: "boolean",
          description: "Force x-vector-only voice cloning when true.",
        },
        language: {
          type: "string",
          description: "Optional language hint for the backend.",
        },
        mode: {
          type: "string",
          enum: ["auto", "voice_clone", "custom_voice", "voice_design"],
          description: "Optional backend synthesis mode.",
        },
        speaker: {
          type: "string",
          description: "Optional speaker id for custom-voice models.",
        },
        instruct: {
          type: "string",
          description: "Optional natural-language style instruction for voice-design models.",
        },
        speed: {
          type: "number",
          description: "Playback speed multiplier, 0.25 to 4.0.",
        },
      },
      required: ["text"],
    },
    async execute(_toolCallId, params) {
      const route = resolveConfiguredTTSRoute(api.config);
      if (!route) {
        return toTextResult("No configured AIMA/OpenClaw TTS route is available.", {
          status: "failed",
          error: "no configured TTS route",
        });
      }

      const roots = workspaceRoots(ctx);
      const rawReferencePath = readStringParam(params, ["referenceAudioPath", "reference_audio_path", "reference_audio", "audioPath", "audio_path"]);
      const resolvedReferencePath = rawReferencePath ? resolveCandidatePath(rawReferencePath, ctx) : "";
      if (rawReferencePath) {
        const validationError = validateInputAudioPath(resolvedReferencePath, roots);
        if (validationError) {
          return toTextResult(validationError, {
            status: "failed",
            error: validationError,
            requestedPath: rawReferencePath,
          });
        }
      }

      const responseFormat = asString(params?.responseFormat || params?.response_format || "wav").toLowerCase() || "wav";
      const outputPath = resolveOutputPath(readStringParam(params, ["outputPath", "output_path", "path"]), ctx, responseFormat);
      const outputValidationError = validateOutputPath(outputPath, roots);
      if (outputValidationError) {
        return toTextResult(outputValidationError, {
          status: "failed",
          error: outputValidationError,
          requestedPath: readStringParam(params, ["outputPath", "output_path", "path"]),
        });
      }

      let referenceText = readStringParam(params, ["referenceText", "reference_text"]);
      let transcription = null;
      let xVectorOnlyMode = asOptionalBoolean(params?.xVectorOnlyMode ?? params?.x_vector_only_mode, false);
      const referenceAudioValue = resolvedReferencePath ? encodeAudioFileAsDataURL(resolvedReferencePath) : "";
      if (resolvedReferencePath && !referenceText) {
        try {
          transcription = await transcribeWithAIMA(api.config, resolvedReferencePath);
          referenceText = transcription.transcript;
        } catch (error) {
          xVectorOnlyMode = true;
        }
      }

      try {
        const request = buildTTSRequest(route, {
          ...params,
          xVectorOnlyMode,
        }, referenceAudioValue, referenceText);
        const result = await synthesizeWithAIMA(route, request);
        fs.mkdirSync(path.dirname(outputPath), { recursive: true });
        fs.writeFileSync(outputPath, Buffer.from(result.audio_base64, "base64"));

        const details = {
          status: "ok",
          path: outputPath,
          model: route.model,
          baseUrl: route.baseUrl,
          responseFormat: result.format || request.response_format,
          durationSeconds: result.duration_seconds,
          mode: result.mode || request.mode || "auto",
          referenceAudioPath: resolvedReferencePath || "",
          referenceText: referenceText || "",
          transcriptionModel: transcription?.model || "",
          xVectorOnlyMode: request.x_vector_only_mode === true,
        };
        return toTextResult(`Audio saved to ${outputPath}`, details);
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        return toTextResult(`Audio synthesis failed: ${message}`, {
          status: "failed",
          path: outputPath,
          error: message,
          referenceAudioPath: resolvedReferencePath || "",
          referenceText: referenceText || "",
        });
      }
    },
  }), { name: "audio_synthesize" });
}
