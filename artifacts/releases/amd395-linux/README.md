# AIMA AMD395 Linux package

This directory contains the distributable AIMA package validated for AMD Ryzen
AI Max+ 395 Linux and the Qwen3.6-35B-A3B native engine.

## Package

- Archive: `aima-amd395-linux-v0.5-dev-72b31ead3a1d.zip`
- Archive SHA-256:
  `c44cd5c944fbf241dcefb0f3f07fb4119f63834a97cc9c6385f9f991263c9edf`
- Source commit: `72b31ead3a1d3e6dd3d50492b1ceb70eedb1cd3d`
- AIMA binary SHA-256:
  `d7a48ec1d4a4eb3c92bcbf69578f51f7b7eacc687a8f000500238c8f97e45573`
- Target: Linux x86-64, AMD Strix Halo / Ryzen AI Max+ 395

The ZIP contains the static AIMA executable, `checksums.txt`, and
`build-metadata.json`. Verify both the outer ZIP checksum and the inner binary
checksum before use.

## Engine contract

The embedded catalog pulls `aima-engine-native 1.4.1-native` from
`aima-engine-native-portable-b98b7bc698ae.tar.zst` and verifies SHA-256
`f75562537277af8b3a0e1a92fb012761a1522b7021f3014bc1f5b8355f650d1b`.

## AMD395 qualification

The exact ZIP in this directory was copied to an AMD Ryzen AI Max+ 395 Linux
host, verified, extracted into a clean install directory, and run with a fresh
AIMA data directory. Qualification passed:

- inner checksum and embedded source metadata;
- catalog-driven v1.4.1 engine download and checksum verification;
- engine `doctor` with all required checks passing;
- BF16 Qwen3.6-35B-A3B model discovery and native deployment;
- AIMA `/health` and `/v1/models`;
- short non-streaming chat (`PACKAGE_UAT_OK`);
- Chinese multi-turn recall (`星河82`);
- SSE content `7,8,9` followed by `[DONE]`;
- exact prompt-cache replay (`miss` then `exact`), with measured TTFT changing
  from about 527 ms to 3.6 ms.

## Use

After extracting the ZIP and making the AIMA binary executable:

```bash
export AIMA_MODEL_DIR=/path/to/models
export AIMA_DATA_DIR=/path/to/aima-data

./aima-linux-amd64-v0.5-dev-amd-strix-halo-72b31ead3a1d model scan
./aima-linux-amd64-v0.5-dev-amd-strix-halo-72b31ead3a1d engine pull \
  aima-amd395-qwen36-native
./aima-linux-amd64-v0.5-dev-amd-strix-halo-72b31ead3a1d deploy \
  qwen3.6-35b-a3b --engine aima-amd395-qwen36-native --no-pull
./aima-linux-amd64-v0.5-dev-amd-strix-halo-72b31ead3a1d serve
```

The OpenAI-compatible endpoint is `http://127.0.0.1:6188/v1` and the public
model id is `qwen3.6-35b-a3b`.
