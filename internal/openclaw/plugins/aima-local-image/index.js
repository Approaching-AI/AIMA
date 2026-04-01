import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const rememberedGeneratedImages = new Map();
const maxRememberedGeneratedImages = 128;
const rememberedImageTtlMs = 10 * 60 * 1000;
const rememberedImageStatePath = path.join(os.homedir(), ".openclaw", "state", "aima-local-image.json");

function asString(value) {
  return typeof value === "string" ? value.trim() : "";
}

function parseModelRef(raw) {
  const value = asString(raw);
  const slash = value.indexOf("/");
  if (slash <= 0 || slash >= value.length - 1) return null;
  return {
    provider: value.slice(0, slash).trim(),
    model: value.slice(slash + 1).trim(),
  };
}

function parseModelRefs(raw) {
  if (typeof raw === "string") {
    const ref = parseModelRef(raw);
    return ref ? [ref] : [];
  }
  if (!raw || typeof raw !== "object") return [];

  const refs = [];
  const primary = parseModelRef(raw.primary);
  if (primary) refs.push(primary);

  if (Array.isArray(raw.fallbacks)) {
    for (const fallback of raw.fallbacks) {
      const ref = parseModelRef(fallback);
      if (ref) refs.push(ref);
    }
  }
  return refs;
}

function unique(values) {
  return [...new Set(values.filter(Boolean))];
}

function contextLookupKeys(ctx) {
  return unique([
    `run:${asString(ctx?.runId)}`,
    `session:${asString(ctx?.sessionId)}`,
    `sessionKey:${asString(ctx?.sessionKey)}`,
  ]);
}

function normalizeLocalImagePath(raw) {
  const value = asString(raw);
  if (!value) return "";
  if (value === "~") return os.homedir();
  if (value.startsWith("~/") || value.startsWith("~\\")) {
    return path.join(os.homedir(), value.slice(2));
  }
  if (value.startsWith("~")) return path.join(os.homedir(), value.slice(1));
  return value;
}

function normalizeRememberedPaths(paths) {
  const normalized = [];
  for (const raw of paths) {
    const value = normalizeLocalImagePath(raw);
    if (!value || !fs.existsSync(value)) continue;
    normalized.push(value);
  }
  return unique(normalized);
}

function readRememberedImageState() {
  try {
    const raw = fs.readFileSync(rememberedImageStatePath, "utf8");
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return {};
    return parsed;
  } catch {
    return {};
  }
}

function writeRememberedImageState(state) {
  fs.mkdirSync(path.dirname(rememberedImageStatePath), { recursive: true });
  fs.writeFileSync(rememberedImageStatePath, JSON.stringify(state, null, 2));
}

function rememberGeneratedImagesInMemory(ctx, paths) {
  const remembered = unique(paths);
  if (remembered.length === 0) return;

  for (const key of contextLookupKeys(ctx)) {
    rememberedGeneratedImages.delete(key);
    rememberedGeneratedImages.set(key, remembered);
  }

  while (rememberedGeneratedImages.size > maxRememberedGeneratedImages) {
    const oldestKey = rememberedGeneratedImages.keys().next().value;
    if (!oldestKey) break;
    rememberedGeneratedImages.delete(oldestKey);
  }
}

function trimRememberedImageStateKeys(keys) {
  const entries = Object.entries(keys ?? {}).filter(([, entry]) => entry && typeof entry === "object");
  entries.sort((left, right) => {
    const leftUpdatedAt = Number(left[1]?.updatedAt) || 0;
    const rightUpdatedAt = Number(right[1]?.updatedAt) || 0;
    return rightUpdatedAt - leftUpdatedAt;
  });
  return Object.fromEntries(entries.slice(0, maxRememberedGeneratedImages));
}

function rememberGeneratedImagesOnDisk(ctx, paths) {
  const remembered = normalizeRememberedPaths(paths);
  if (remembered.length === 0) return;

  const updatedAt = Date.now();
  const state = readRememberedImageState();
  const keys = state?.keys && typeof state.keys === "object" ? { ...state.keys } : {};

  for (const key of contextLookupKeys(ctx)) {
    keys[key] = {
      paths: remembered,
      updatedAt,
    };
  }

  writeRememberedImageState({
    latest: {
      paths: remembered,
      updatedAt,
    },
    keys: trimRememberedImageStateKeys(keys),
  });
}

function rememberedPathsFromStateEntry(entry) {
  if (!entry || typeof entry !== "object") return [];
  return normalizeRememberedPaths(Array.isArray(entry.paths) ? entry.paths : []);
}

function rememberedStateEntryIsFresh(entry) {
  const updatedAt = Number(entry?.updatedAt);
  if (!Number.isFinite(updatedAt) || updatedAt <= 0) return false;
  return Date.now() - updatedAt <= rememberedImageTtlMs;
}

function rememberGeneratedImages(ctx, paths) {
  const remembered = normalizeRememberedPaths(paths);
  if (remembered.length === 0) return;

  rememberGeneratedImagesInMemory(ctx, remembered);
  rememberGeneratedImagesOnDisk(ctx, remembered);
}

function findRememberedGeneratedImages(ctx) {
  for (const key of contextLookupKeys(ctx)) {
    const remembered = rememberedGeneratedImages.get(key);
    if (remembered?.length) return remembered;
  }

  const state = readRememberedImageState();
  const keys = state?.keys && typeof state.keys === "object" ? state.keys : {};
  for (const key of contextLookupKeys(ctx)) {
    const remembered = rememberedPathsFromStateEntry(keys[key]);
    if (remembered.length === 0) continue;
    rememberGeneratedImagesInMemory(ctx, remembered);
    return remembered;
  }

  if (rememberedStateEntryIsFresh(state?.latest)) {
    const remembered = rememberedPathsFromStateEntry(state.latest);
    if (remembered.length > 0) {
      rememberGeneratedImagesInMemory(ctx, remembered);
      return remembered;
    }
  }

  return [];
}

function isResolvableImageReference(raw) {
  const value = asString(raw);
  if (!value) return false;
  if (/^(https?:\/\/|data:|file:)/i.test(value)) return true;
  return fs.existsSync(normalizeLocalImagePath(value));
}

function extractImageToolCandidates(params) {
  const record = params && typeof params === "object" ? params : {};
  const values = [];

  const image = asString(record.image);
  if (image) values.push(image);

  const images = Array.isArray(record.images) ? record.images : [];
  for (const raw of images) {
    const image = asString(raw);
    if (image) values.push(image);
  }

  return unique(values);
}

function rewriteImageToolParams(params, rememberedPaths) {
  const record = params && typeof params === "object" ? params : {};
  const candidates = extractImageToolCandidates(record);
  const hasResolvableCandidate = candidates.some((candidate) => isResolvableImageReference(candidate));
  if (hasResolvableCandidate) return null;

  const fallbackImage = asString(rememberedPaths[0]);
  if (!fallbackImage) return null;

  return {
    ...record,
    image: fallbackImage,
    images: [],
  };
}

function resolveImageGenerationSpec(cfg) {
  const providerId = "aima-imagegen";
  const provider = resolveProviderConfig(cfg, providerId);
  if (!provider) return null;

  const refs = parseModelRefs(cfg?.agents?.defaults?.imageGenerationModel).filter(
    (ref) => ref.provider === providerId,
  );
  const models = unique(refs.map((ref) => ref.model));
  if (models.length === 0) return null;

  return {
    ...provider,
    id: providerId,
    defaultModel: models[0],
    models,
  };
}

function resolveProviderConfig(cfg, providerId) {
  const provider = cfg?.models?.providers?.[providerId];
  if (!provider || typeof provider !== "object") return null;
  const baseUrl = asString(provider.baseUrl);
  if (!baseUrl) return null;
  return {
    baseUrl: baseUrl.replace(/\/+$/, ""),
    apiKey: asString(provider.apiKey),
  };
}

function readSize(req) {
  const size = asString(req?.size);
  return size || "512x512";
}

function buildImageResults(payload) {
  const data = Array.isArray(payload?.data) ? payload.data : [];
  return data
    .map((entry, index) => {
      const b64 = asString(entry?.b64_json);
      if (!b64) return null;
      const image = {
        buffer: Buffer.from(b64, "base64"),
        mimeType: "image/png",
        fileName: `image-${index + 1}.png`,
      };
      const revisedPrompt = asString(entry?.revised_prompt);
      if (revisedPrompt) image.revisedPrompt = revisedPrompt;
      return image;
    })
    .filter((image) => image !== null);
}

function errorMessage(payload, status) {
  return (
    asString(payload?.error?.message) ||
    asString(payload?.error) ||
    `HTTP ${status}`
  );
}

function collectGeneratedImagePaths(message) {
  if (!message || typeof message !== "object") return [];

  const values = [];
  const detailsPaths = Array.isArray(message?.details?.paths) ? message.details.paths : [];
  for (const raw of detailsPaths) {
    const path = asString(raw);
    if (path) values.push(path);
  }

  const mediaUrls = Array.isArray(message?.details?.media?.mediaUrls)
    ? message.details.media.mediaUrls
    : [];
  for (const raw of mediaUrls) {
    const path = asString(raw);
    if (path) values.push(path);
  }

  return unique(values);
}

function copyGeneratedImageToWorkspace(srcPath) {
  const source = asString(srcPath);
  if (!source) return "";

  const workspaceDir = path.join(os.homedir(), ".openclaw", "workspace", "media");
  const fileName = path.basename(source);
  if (!fileName) return source;

  const dest = path.join(workspaceDir, fileName);
  if (source === dest) return dest;

  fs.mkdirSync(workspaceDir, { recursive: true });
  fs.copyFileSync(source, dest);
  return dest;
}

function ensureWorkspaceImagePaths(paths) {
  const copied = [];
  for (const raw of paths) {
    try {
      const dest = copyGeneratedImageToWorkspace(raw);
      if (dest) copied.push(dest);
    } catch {
      const fallback = asString(raw);
      if (fallback) copied.push(fallback);
    }
  }
  return unique(copied);
}

function patchImageGenerateTranscript(message) {
  if (!message || typeof message !== "object") return null;

  const paths = ensureWorkspaceImagePaths(collectGeneratedImagePaths(message));
  if (paths.length === 0) return null;

  const pathLabel = paths.length === 1 ? "Saved image path" : "Saved image paths";
  const examplePath = JSON.stringify(paths[0]);
  const guidance = [
    `${pathLabel}:`,
    paths.join("\n"),
    "For follow-up image analysis, call image with exactly this argument shape:",
    `{"image":${examplePath},"prompt":"Describe the actual content in this image"}`,
    "The image argument is required and must be one of the saved local paths above.",
    "Do not use placeholder paths like /tmp/tmp.jpg or /path/to/generated-image.jpg.",
  ].join("\n");
  const details = message.details && typeof message.details === "object" ? { ...message.details } : {};
  const media = details.media && typeof details.media === "object" ? { ...details.media } : {};
  details.paths = paths;
  media.mediaUrls = paths;
  details.media = media;
  const content = Array.isArray(message.content) ? message.content.slice() : [];

  for (let i = 0; i < content.length; i += 1) {
    const entry = content[i];
    if (entry?.type !== "text") continue;

    const text = asString(entry.text);
    if (text.includes(paths[0])) {
      return null;
    }

    content[i] = { ...entry, text: `${text}\n${guidance}`.trim() };
    return { ...message, content, details };
  }

  content.push({ type: "text", text: guidance });
  return { ...message, content, details };
}

export default function register(api) {
  api.on("after_tool_call", async (event, ctx) => {
    if (asString(event?.toolName) !== "image_generate") return;

    const paths = ensureWorkspaceImagePaths(collectGeneratedImagePaths(event?.result));
    if (paths.length === 0) return;

    rememberGeneratedImages(ctx, paths);
  });

  api.on("before_tool_call", async (event, ctx) => {
    if (asString(event?.toolName) !== "image") return;

    const rememberedPaths = findRememberedGeneratedImages(ctx);
    if (rememberedPaths.length === 0) return;

    const nextParams = rewriteImageToolParams(event?.params, rememberedPaths);
    if (!nextParams) return;

    return { params: nextParams };
  });

  api.on("tool_result_persist", (event, ctx) => {
    const toolName = asString(event?.toolName || event?.message?.toolName);
    if (toolName !== "image_generate") return;

    const nextMessage = patchImageGenerateTranscript(event?.message);
    if (!nextMessage) return;

    const paths = ensureWorkspaceImagePaths(collectGeneratedImagePaths(nextMessage));
    if (paths.length > 0) rememberGeneratedImages(ctx, paths);

    return { message: nextMessage };
  });

  api.registerImageGenerationProvider({
    id: "aima-imagegen",
    label: "AIMA Local Image",
    get defaultModel() {
      return resolveImageGenerationSpec(api.config)?.defaultModel || "";
    },
    get models() {
      return resolveImageGenerationSpec(api.config)?.models || [];
    },
    capabilities: {
      generate: {
        maxCount: 1,
        supportsSize: true,
        supportsAspectRatio: false,
        supportsResolution: false,
      },
      edit: {
        enabled: false,
        maxCount: 0,
        maxInputImages: 0,
        supportsSize: false,
        supportsAspectRatio: false,
        supportsResolution: false,
      },
      geometry: {
        sizes: ["512x512"],
      },
    },
    async generateImage(req) {
      if ((req.inputImages?.length ?? 0) > 0) {
        throw new Error("AIMA local image provider does not support reference-image edits");
      }

      const prompt = asString(req.prompt);
      if (!prompt) throw new Error("prompt required");

      const spec = resolveImageGenerationSpec(req.cfg ?? api.config);
      if (!spec) {
        throw new Error("AIMA local image provider is not configured");
      }

      const headers = { "Content-Type": "application/json" };
      if (spec.apiKey) {
        headers.Authorization = `Bearer ${spec.apiKey}`;
      }

      const response = await fetch(`${spec.baseUrl}/images/generations`, {
        method: "POST",
        headers,
        body: JSON.stringify({
          model: req.model || spec.defaultModel,
          prompt,
          n: req.count ?? 1,
          size: readSize(req),
          response_format: "b64_json",
        }),
      });

      const payload = await response.json().catch(() => null);
      if (!response.ok) {
        throw new Error(`image generation failed: ${errorMessage(payload, response.status)}`);
      }

      const images = buildImageResults(payload);
      if (images.length === 0) {
        throw new Error("image generation response missing b64_json");
      }

      return {
        images,
        model: req.model || spec.defaultModel,
      };
    },
  });
}
