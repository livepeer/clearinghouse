---
title: "Run a job on the network"
description: "Find an app on the network, call it through the SDK, and point it at a signer to pay — no wallet, no crypto in your app."
---

> **Developer docs source — Tier 1.** Illustrative walkthrough of the target developer experience. Code reflects the current [`livepeer-python-gateway`](https://github.com/livepeer/livepeer-python-gateway) SDK; names may still change.

Three moves: **find** an app, **call** it through the SDK, point it at a **signer** to pay. Your media flows straight to the orchestrator; the payment layer never sees it — it's just a URL and a key.

## 1. Find an app to run

An *app* is a container that runs on the network — a realtime video pipeline, an LLM, an image model, or anything you bring yourself. Orchestrators host your container and pass traffic straight through over **HTTP, SSE, WebSocket, or trickle** (Livepeer's segment-based transport for realtime media), so you can lift an app off your old compute provider with little change: the orchestrator pulls your image and runs it on its GPUs.

The runtime is in **beta** — there's no self-serve onboarding yet, so to put *your own* app on the network you [contact the team](#bring-your-own-app). You can already run the **open-source example apps** live today. Find them two ways:

- **Dashboard** — [livepeer.org/dashboard](https://livepeer.org/dashboard), a small hosted app for trying the network.
- **Discovery endpoint** — [`GET /v1/discovery/capabilities`](https://discovery-service-production-8955.up.railway.app/docs#tag/discovery/GET/v1/discovery/capabilities) lists the available apps and models.

Each app has an **app id** (e.g. `livepeer-sample/hello-world`) — that's all the SDK needs to route to it.

## 2. Install the SDK

Not on PyPI yet — install from the `ja/live-runner` branch:

```bash
pip install "git+https://github.com/livepeer/livepeer-python-gateway@ja/live-runner"
```

## 3. Run a job

`reserve_session` handles discovery and orchestrator selection from an app id — the same call for every app. What you do *after* it depends on the app's transport. Start with `hello-world`, a plain HTTP request/response app:

```python
from livepeer_gateway.selection import reserve_session
from livepeer_gateway.live_runner import call_runner, stop_runner_session

SIGNER_URL, SIGNER_HEADERS = "https://api.pymthouse.com", {"Authorization": "Bearer sk_live_..."}  # see §4

session = await reserve_session(
    discovery_url="https://localhost:8935/discovery",
    app="livepeer-sample/hello-world",
    signer_url=SIGNER_URL, signer_headers=SIGNER_HEADERS,
)
result = await call_runner(
    runner_url=session.app_url.rstrip("/") + "/hello",
    payload={"name": "livepeer"},
    signer_url=SIGNER_URL, signer_headers=SIGNER_HEADERS,
)
print(result.data)                                          # {'message': 'Hello, livepeer!'}
await stop_runner_session(session)
```

The other example apps keep the same `reserve_session` and swap the transport:

| Example | Transport | App id |
| --- | --- | --- |
| [`hello-world`](https://github.com/livepeer/live-runner-example-apps/tree/main/hello-world) | HTTP request/response | `livepeer-sample/hello-world` |
| [`vllm`](https://github.com/livepeer/live-runner-example-apps/tree/main/vllm) | SSE (token streaming) | `vllm/qwen2.5-0.5b-instruct` |
| [`echo`](https://github.com/livepeer/live-runner-example-apps/tree/main/echo) | trickle (realtime video) | `livepeer-sample/echo` |
| [`streaming-asr`](https://github.com/livepeer/live-runner-example-apps/tree/main/streaming-asr) | WebSocket (speech-to-text) | `whisper/base.en` |

### SSE — streaming tokens (`vllm`)

`vllm` serves an OpenAI-compatible API, so you use the **stock `openai` client** with zero Livepeer code. Payment lives in a small local [`gateway.py`](https://github.com/livepeer/live-runner-example-apps/blob/main/vllm/gateway.py) that does discovery + the ticket handshake for you (forwarding the runner's `text/event-stream` straight through with `call_runner(..., stream=True)`) — point it at your signer once (see §4):

```bash
uv run gateway.py --signer https://api.pymthouse.com    # OpenAI endpoint on http://localhost:8080/v1
```

Your client is then plain OpenAI against that `base_url`:

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/v1", api_key="unused")  # gateway handles discovery + payment
stream = client.chat.completions.create(
    model="Qwen/Qwen2.5-0.5B-Instruct",
    messages=[{"role": "user", "content": "write a haiku about GPUs"}],
    stream=True,
)
for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="", flush=True)   # tokens arrive as generated
```

### Trickle — realtime video (`echo`)

POST to the app's endpoint to open its trickle `in`/`out` channels, then publish frames to `in` and read transformed frames from `out`, keeping the long-lived session funded in the background:

```python
from livepeer_gateway.http import post_json
from livepeer_gateway.media_publish import MediaPublish
from livepeer_gateway.media_output import MediaOutput

session = await reserve_session(discovery_url="https://localhost:8935/discovery",
                                app="livepeer-sample/echo",
                                signer_url=SIGNER_URL, signer_headers=SIGNER_HEADERS)
session.start_payments()        # tops up the session balance while it stays open

channels = await post_json(session.app_url.rstrip("/") + "/echo", {"radius": 75})  # → {"in": ..., "out": ...}
publisher = MediaPublish(channels["in"])                    # publish your frames here
async with MediaOutput(channels["out"], on_bytes=write):    # read transformed frames back
    ...                                                     # pump frames — see the echo client
await session.aclose()          # stops payments + releases the session
```

### WebSocket — speech-to-text (`streaming-asr`)

`open_websocket` opens the orchestrator-proxied socket from a reserved session and keeps it funded — stream audio up, get transcripts back on the same socket:

```python
from livepeer_gateway.live_runner import open_websocket

session = await reserve_session(discovery_url="https://localhost:8935/discovery",
                                app="whisper/base.en",
                                signer_url=SIGNER_URL, signer_headers=SIGNER_HEADERS)
async with open_websocket(session, "/transcribe") as ws:   # SDK does the wss upgrade + session payments
    await ws.send_bytes(pcm_16khz_mono)         # stream 16 kHz mono PCM up (your audio bytes)
    await ws.send_str("eos")                    # signal end of audio
    async for msg in ws:                        # transcripts stream back
        print(msg.json())                       # {"text": "...", "final": false|true}
```

## 4. Pay for the job — the signer step

Livepeer is a **decentralized network**: jobs are paid by sending the orchestrator a probabilistic-micropayment ticket that settles on-chain in ETH. Rather than hold a hot wallet in your app, you point it at a **signer** that mints tickets for you — and a **clearinghouse** can sit on top to handle accounts and billing in plain dollars. `signer_url` + `signer_headers` is the *only* payment config your app needs — no wallet, no crypto in your code.

**Fastest path: a community-hosted signer.** [PymtHouse](https://pymthouse.com) hosts the signer and accounting — sign up, get a key, drop it in:

```python
SIGNER_URL = "https://api.pymthouse.com"
SIGNER_KEY = "sk_live_..."
```

It meters usage and bills your account; when the balance runs out, jobs stop. Switching providers is just a different URL and key.

**Run it yourself?** The [Payment Provider](./payment-provider.md) docs cover the whole stack:

- **[Run a remote signer](./payment-provider.md#run-a-remote-signer)** — mint your own tickets for your own jobs, no accounts.
- **[Run the clearinghouse suite](./payment-provider.md#run-the-clearinghouse-suite)** — the accounting layer on top, to give customers a balance and bill them.

The clearinghouse is a **suite**: a lean core with swappable ports, so you plug in established solutions instead of rolling your own — [OpenMeter](https://openmeter.io) for metering/billing, OAuth/OIDC for auth, Stripe / USDC for funding.

## Bring your own app

Start from [`live-runner-example-apps`](https://github.com/livepeer/live-runner-example-apps) — each example runs locally with `docker compose` against a local orchestrator, so you can build and test your app end to end on your own GPUs **for free**: offchain, omit the signer entirely (`reserve_session(discovery_url="https://localhost:8935/discovery", app=...)`) — it's only needed to pay real orchestrators on the network. Once it's running locally and you want it live, reach out at [partnerships@livepeer.foundation](mailto:partnerships@livepeer.foundation) and we'll onboard it.
