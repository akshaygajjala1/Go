# Pulse

**Dead-simple analytics for bots and side projects. One line to install, see your events live. Free, open source, self-hostable.**

> Not a PostHog competitor. The opposite — the absolute minimum. One Go service, one Redis instance, runs on a $5 box, sets up in two minutes. It counts your events and shows them. Nothing else.

*(Pulse is a placeholder name — swap in whatever you land on.)*

---

## Table of contents

- [Why this exists](#why-this-exists)
- [What it is and isn't](#what-it-is-and-isnt)
- [Quickstart](#quickstart)
- [How it works](#how-it-works)
- [Architecture](#architecture)
- [API reference](#api-reference)
- [SDK usage](#sdk-usage)
- [Self-hosting](#self-hosting)
- [Configuration](#configuration)
- [Performance](#performance)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)

---

## Why this exists

Small projects fly blind. You ship a side project or run a Discord bot and have no real idea what's happening inside it — how many people ran a command, which features get used, when errors started spiking. You usually find out something broke because a user messages you, not because you saw it.

The existing tools don't fit this situation:

- **Google Analytics** is built for marketing pages and pageviews, not arbitrary backend events.
- **PostHog / Plausible** are genuinely good, but they're full platforms. Self-hosting PostHog means running ClickHouse, Postgres, Kafka, and several services — wildly overkill when you just want to count some events on a project with a handful of users.

Pulse is the simple version that didn't exist. Send an event, see it on a dashboard. That's the whole thing.

---

## What it is and isn't

**It is:**
- A single Go service plus Redis.
- A one-line SDK call to record an event.
- A live dashboard showing counts and trends over time.
- Light enough to run on the smallest instance you can rent and forget about.

**It is not:**
- A replacement for PostHog or Plausible.
- Session recording, feature flags, A/B testing, or funnels.
- A system for infinite event history or complex ad-hoc querying.

The scope discipline is the point. The moment it grows a second backing service, it loses its only real advantage. If you want the heavy toolkit, use PostHog. If you want to see your events without running five services, use this.

---

## Quickstart

### 1. Track an event

```js
import { pulse } from 'pulse-sdk';

pulse.init('YOUR_PROJECT_KEY');
pulse.track('command_run', { command: 'play' });
```

### 2. See it

Open your dashboard at `https://your-instance/dashboard`. The event shows up within a second.

That's it. No tag manager, no config files, no pricing page.

---

## How it works

A high-level trace of a single event:

1. Your code calls `pulse.track('signup')`.
2. The SDK sends a small JSON payload to `POST /v1/event`.
3. The ingestion server validates it, authenticates the project key, and drops it onto an in-memory buffer (a Go channel). The HTTP handler returns immediately — it does not wait for storage.
4. A pool of worker goroutines pulls events off the buffer and writes aggregates to Redis (incrementing counters and updating time-bucketed sorted sets).
5. When the dashboard asks `GET /v1/stats`, the query layer reads those aggregates from Redis and returns them.

The key design decision: ingestion and storage are decoupled by the buffer, so a burst of traffic doesn't block incoming requests or fall over. When the buffer fills, the system sheds load deliberately rather than crashing.

---

## Architecture

```
                  +------------------+
   your bot /     |   Pulse SDK      |
   side project   |  (JS / Python)   |
                  +--------+---------+
                           |  POST /v1/event
                           v
                  +------------------+
                  |  Ingestion API   |   Go, net/http
                  |  - auth (API key)|
                  |  - validation    |
                  |  - rate limiting |
                  +--------+---------+
                           |  buffered channel
                           v
                  +------------------+
                  |  Worker pool     |   goroutines
                  |  - aggregation   |
                  |  - backpressure  |
                  +--------+---------+
                           |
                           v
                  +------------------+
                  |      Redis       |
                  |  - counters      |   hashes
                  |  - time series   |   sorted sets
                  |  - rate limits   |   TTL keys
                  +--------+---------+
                           ^
                           |  GET /v1/stats
                  +--------+---------+
                  |   Query API      |   Go
                  +--------+---------+
                           ^
                           |
                  +------------------+
                  |   Dashboard      |   React
                  +------------------+

   Self-monitoring: the service exposes /metrics (Prometheus),
   visualized in Grafana. Deployed as a container on AWS ECS Fargate
   with a CI/CD pipeline via GitHub Actions.
```

### Components

| Component | Tech | Responsibility |
|-----------|------|----------------|
| Ingestion API | Go (`net/http`) | Accept, authenticate, validate, and buffer events |
| Worker pool | Go goroutines + channels | Drain the buffer, aggregate, handle backpressure |
| Storage | Redis | Counters (hashes), time-series (sorted sets), rate-limit state (TTL keys) |
| Query API | Go | Serve aggregated stats to the dashboard |
| SDK | JS / Python | One-line client; wrappers for discord.js and discord.py |
| Dashboard | React | Project picker, live charts, event breakdowns |
| Observability | Prometheus + Grafana | The service monitors itself |

---

## API reference

### `POST /v1/event`

Record a single event.

**Headers**
```
Authorization: Bearer YOUR_PROJECT_KEY
Content-Type: application/json
```

**Body**
```json
{
  "name": "command_run",
  "properties": { "command": "play" },
  "timestamp": 1718900000
}
```

`timestamp` is optional; the server stamps it if omitted. `properties` is optional and limited in size.

**Responses**
- `202 Accepted` — event buffered.
- `400 Bad Request` — malformed payload.
- `401 Unauthorized` — bad or missing project key.
- `429 Too Many Requests` — project rate limit exceeded.

### `POST /v1/events`

Batch version. Accepts an array of event objects under an `events` key. Same auth and limits. Recommended for high-volume clients to cut request overhead.

### `GET /v1/stats`

Return aggregated stats for the authenticated project.

**Query params**
- `event` — filter to a single event name (optional).
- `window` — one of `1h`, `24h`, `7d` (default `24h`).
- `resolution` — bucket size: `minute`, `hour`, `day`.

**Response**
```json
{
  "event": "command_run",
  "window": "24h",
  "total": 1840,
  "series": [
    { "bucket": 1718896400, "count": 73 },
    { "bucket": 1718900000, "count": 91 }
  ]
}
```

### `GET /healthz`

Liveness check. Returns `200` when the service and its Redis connection are healthy.

### `GET /metrics`

Prometheus metrics: ingestion rate, buffer depth, worker throughput, query latency, dropped-event count.

---

## SDK usage

### JavaScript / TypeScript

```js
import { pulse } from 'pulse-sdk';

pulse.init('YOUR_PROJECT_KEY', { endpoint: 'https://your-instance' });

pulse.track('signup');
pulse.track('command_run', { command: 'play', guild: 'abc' });
```

### Python

```python
from pulse import Pulse

pulse = Pulse('YOUR_PROJECT_KEY', endpoint='https://your-instance')

pulse.track('signup')
pulse.track('command_run', {'command': 'play'})
```

### Discord bot wrappers

The point of the project is that install takes one line for the target audience, so the SDK ships thin wrappers for the common bot frameworks.

**discord.js**
```js
import { pulseDiscord } from 'pulse-sdk/discord';

pulseDiscord(client, 'YOUR_PROJECT_KEY');
```
This auto-tracks command invocations, guild joins/leaves, and errors. You can still call `pulse.track()` manually for anything custom.

**discord.py**
```python
from pulse.discord import attach

attach(bot, 'YOUR_PROJECT_KEY')
```

The SDK batches events client-side and flushes on an interval so a busy bot doesn't make one HTTP call per event.

---

## Self-hosting

The entire system is two processes: the Go service and Redis. Run it locally or on any small box.

### Docker Compose (recommended)

```yaml
services:
  pulse:
    image: pulse:latest
    ports:
      - "8080:8080"
    environment:
      - REDIS_ADDR=redis:6379
    depends_on:
      - redis
  redis:
    image: redis:7-alpine
```

```bash
docker compose up
```

The service is now on `localhost:8080` and the dashboard on `localhost:8080/dashboard`.

### From source

```bash
git clone https://github.com/YOURNAME/pulse
cd pulse
go build -o pulse ./cmd/pulse
REDIS_ADDR=localhost:6379 ./pulse
```

### Deployed (AWS)

The reference deployment runs the container on AWS ECS Fargate, with Redis on ElastiCache (or a small self-managed Redis container to stay free). A GitHub Actions workflow builds the image, pushes to ECR, and rolls out on merge to `main`. See `deploy/` for the task definitions and the workflow file.

---

## Configuration

All configuration is via environment variables.

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP port |
| `REDIS_ADDR` | `localhost:6379` | Redis host:port |
| `REDIS_PASSWORD` | _(empty)_ | Redis auth, if set |
| `BUFFER_SIZE` | `10000` | In-memory event buffer capacity before load shedding |
| `WORKER_COUNT` | `8` | Number of processing goroutines |
| `RATE_LIMIT_RPS` | `100` | Per-project requests/sec before `429` |
| `EVENT_TTL_DAYS` | `30` | How long time-series buckets are retained |
| `MAX_PROPERTIES_BYTES` | `1024` | Max size of an event's `properties` object |

---

## Performance

Pulse is designed to do a lot on very little. The buffer-plus-worker-pool design means a single small instance absorbs bursts without dropping requests, and Redis handles the aggregation work cheaply.

Benchmarked on [INSTANCE TYPE — fill in], single instance:

| Metric | Result |
|--------|--------|
| Sustained ingestion | [FILL IN] events/sec |
| p99 ingestion latency | [FILL IN] ms |
| p99 query latency | [FILL IN] ms |
| Memory footprint | [FILL IN] MB |

Reproduce with the load test in `loadtest/` (uses [tool — k6 / Locust / wrk]).

> Replace every bracketed value with real measured numbers before publishing. Vague or invented numbers are the fastest way to lose credibility — and you can't defend them in an interview.

---

## Roadmap

Deliberately short. The risk is feature creep killing the lightness.

- [ ] Batch ingestion endpoint
- [ ] discord.js + discord.py wrappers
- [ ] Dashboard: per-property breakdowns
- [ ] Simple alerting (notify when an event stops firing or error count spikes)
- [ ] Telegram bot wrapper

Things intentionally **not** planned: session replay, feature flags, A/B testing, SQL querying. 

---

## Contributing

Issues and PRs welcome. The project values staying small — features that add a new backing service or meaningfully grow the footprint will likely be declined, and that's by design. Good first contributions: SDK wrappers for more frameworks, dashboard polish, docs.

---

## License

MIT. Host it yourself, fork it, do what you want.