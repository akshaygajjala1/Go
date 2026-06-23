# Relay

**A lightweight, self-hostable job queue. One Go binary plus Redis. Workers in any language. Won't lose your jobs when something crashes.**

> The simple thing to reach for when Celery is too Python-specific, RabbitMQ is too much to operate, and SQS means locking into AWS. Relay is one binary and a Redis instance. It sets up in two minutes and takes failure seriously — if a worker dies mid-job, the job comes back.

*(Relay is a placeholder name — swap in whatever you land on.)*

---

## Table of contents

- [Why this exists](#why-this-exists)
- [What it is and isn't](#what-it-is-and-isnt)
- [Core idea: leases](#core-idea-leases)
- [Quickstart](#quickstart)
- [How it works](#how-it-works)
- [Architecture](#architecture)
- [Delivery semantics](#delivery-semantics)
- [API reference](#api-reference)
- [Worker protocol](#worker-protocol)
- [SDK usage](#sdk-usage)
- [Self-hosting](#self-hosting)
- [Configuration](#configuration)
- [Observability](#observability)
- [Performance](#performance)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)

---

## Why this exists

Every backend eventually needs background jobs — send this email, process this upload, retry this webhook, resize this image — work that shouldn't block the request that triggered it. For small projects, the existing options are all a poor fit:

- **Celery** is powerful but Python-only and genuinely heavy to operate.
- **RabbitMQ / Kafka** are real infrastructure — broker clusters, sometimes Zookeeper — which is absurd overhead for a side project or a bot.
- **SQS / cloud queues** cost money, require an AWS account, and lock you in.

So most small projects either hand-roll something fragile with a database table and a cron loop, or just run the work inline and pray nothing crashes mid-task. When something *does* crash, the job is silently lost.

Relay is the thing you'd actually reach for at this scale: trivial to run, language-agnostic, and serious about not losing your jobs.

---

## What it is and isn't

**It is:**
- A single Go binary plus Redis.
- Language-agnostic — workers in any language pull jobs over a simple HTTP protocol.
- Durable under failure — a job whose worker dies is automatically re-queued.
- Self-hostable in two minutes, with a dashboard to watch what's happening.

**It is not:**
- A Kafka or RabbitMQ replacement at scale. It's for small-to-medium workloads.
- A general pub/sub or event-streaming bus. It's a work queue.
- Exactly-once delivery (nobody truly offers that — see [Delivery semantics](#delivery-semantics)). It's at-least-once with idempotency support.

The lightness and the failure-handling are the entire value. The moment it grows a broker cluster, it's just a worse RabbitMQ.

---

## Core idea: leases

The whole design rests on one concept, and it's worth understanding before anything else.

When a worker takes a job, it doesn't *remove* the job — it takes a **lease** on it. The lease has a deadline. While the worker holds the lease, no other worker can touch the job. The worker must periodically **renew** the lease (a heartbeat) while it works. When the job finishes, the worker **acknowledges** it, and the job is gone for good.

If the worker dies, it stops renewing. The lease expires. Relay notices the expired lease and puts the job back on the queue, where another worker picks it up. Nothing is lost.

This single mechanism — lease, renew, expire, re-queue — is how Relay survives crashes without a complex consensus protocol. It's the same idea behind SQS visibility timeouts and Kubernetes leases.

---

## Quickstart

### 1. Run Relay

```bash
docker run -p 8080:8080 relay
```

### 2. Enqueue a job (any language, just HTTP)

```bash
curl -X POST localhost:8080/v1/jobs \
  -H "Authorization: Bearer YOUR_KEY" \
  -d '{"queue": "emails", "payload": {"to": "user@example.com"}}'
```

### 3. Run a worker

```python
from relay import Worker

worker = Worker('localhost:8080', 'YOUR_KEY')

@worker.handle('emails')
def send_email(payload):
    send(payload['to'])   # your work here

worker.run()
```

If `send_email` raises or the process dies, the job is retried automatically. That's the whole pitch.

---

## How it works

A trace of a single job through the system:

1. **Enqueue.** A client `POST`s a job to a named queue. Relay validates it, assigns a job ID, and pushes it onto the pending list in Redis. Returns `201` with the ID.
2. **Claim.** A worker long-polls `POST /v1/jobs/claim`. Relay atomically moves a job from pending to "leased," stamps a lease deadline, and returns the job to that worker. Atomicity here is critical — two workers must never claim the same job.
3. **Work + heartbeat.** The worker runs the job. While running, it periodically calls `POST /v1/jobs/{id}/renew` to extend the lease, proving it's still alive.
4. **Ack or fail.**
   - On success: the worker calls `POST /v1/jobs/{id}/ack`. The job is removed permanently.
   - On failure: the worker calls `POST /v1/jobs/{id}/fail`, or simply stops. The job is scheduled for retry with backoff.
5. **Reaper.** A background loop in Relay scans for leases whose deadline has passed (worker died without acking or renewing) and moves those jobs back to pending for redelivery.
6. **Dead-letter.** A job that fails more than its max-retries lands in a dead-letter queue for inspection instead of retrying forever.

The interesting engineering is all in steps 2, 5, and the retry logic — atomic claim, lease expiry detection, and safe redelivery. That's the part worth understanding cold.

---

## Architecture

```
   any client          +------------------+
   (any language) ----> |   Enqueue API    |  POST /v1/jobs
                        +--------+---------+
                                 |
                                 v
                        +------------------+
                        |      Redis       |
                        |  pending   (list / sorted set by priority+time)
                        |  leased    (hash: job -> lease deadline)
                        |  retry     (sorted set by next-attempt time)
                        |  dead       (list)
                        +--------+---------+
                          ^   ^          ^
          claim / renew / |   |          | reaper scans
          ack / fail      |   |          | expired leases
                          |   |          |
                +---------+   +-------+   +-----------+
                |                     |               |
        +-------+------+      +-------+------+   +-----+------+
        |   Worker A   |      |   Worker B   |   |  Reaper    |  (goroutine
        | (Python)     |      | (Node)       |   |  loop)     |   inside Relay)
        +--------------+      +--------------+   +------------+

   Relay server (Go): Enqueue API + Claim/Ack/Renew/Fail API + Reaper loop
   + /metrics (Prometheus) + dashboard (React).
   Deployed as a container on AWS ECS Fargate, CI/CD via GitHub Actions.
```

### Components

| Component | Tech | Responsibility |
|-----------|------|----------------|
| Enqueue API | Go (`net/http`) | Accept and validate jobs, push to pending |
| Claim/lease API | Go | Atomic claim, lease renew, ack, fail |
| Reaper | Go goroutine | Detect expired leases, re-queue dead jobs |
| Retry scheduler | Go + Redis sorted set | Hold failed jobs until their next-attempt time |
| Storage | Redis | All queue state — pending, leased, retry, dead |
| SDK | Python / Node / Go | Worker loop: claim, heartbeat, ack/fail |
| Dashboard | React | Queue depths, throughput, failures, dead-letter inspection |
| Observability | Prometheus + Grafana | Relay monitors itself |

---

## Delivery semantics

Be precise about this, because it's the first thing a sharp interviewer or user will probe.

Relay is **at-least-once.** A job is delivered until it's acked. Because a worker can finish a job and then die *before* acking, a job can occasionally run more than once. This is true of essentially every distributed queue — true exactly-once delivery is impossible in the presence of failures; what real systems offer is at-least-once delivery plus a way to make processing idempotent.

Relay's answer:

- **At-least-once by default.** Leases guarantee a job is never lost, at the cost of possible rare duplicates.
- **Idempotency keys.** Each job carries a stable ID. Relay tracks recently-acked IDs for a configurable window, so a duplicate delivery of an already-completed job can be detected and skipped. This gets you "effectively-once" for the common case without claiming the impossible.
- **Visibility/lease timeout** is tunable per queue — short for fast jobs, long for slow ones.

The honest one-liner: *Relay won't lose your jobs. It may, rarely, run one twice — design your handlers to be idempotent, and use the provided idempotency keys.*

---

## API reference

### `POST /v1/jobs`
Enqueue a job.
```json
{
  "queue": "emails",
  "payload": { "to": "user@example.com" },
  "priority": 0,
  "max_retries": 5,
  "idempotency_key": "optional-stable-id"
}
```
Returns `201` with `{ "id": "..." }`.

### `POST /v1/jobs/claim`
Worker claims the next available job from one or more queues. Long-polls if none available.
```json
{ "queues": ["emails"], "lease_seconds": 30 }
```
Returns the job (id, queue, payload, attempt count) or `204` if none.

### `POST /v1/jobs/{id}/renew`
Extend the lease on a held job (heartbeat). Returns the new deadline.

### `POST /v1/jobs/{id}/ack`
Mark a job done. Removed permanently.

### `POST /v1/jobs/{id}/fail`
Report failure. Job is scheduled for retry with backoff, or dead-lettered if out of retries.
```json
{ "reason": "smtp timeout" }
```

### `GET /v1/queues/{name}/stats`
Depth of pending / leased / retry / dead, plus throughput.

### `GET /v1/dead`
List dead-lettered jobs for inspection; supports requeue.

### `GET /healthz` / `GET /metrics`
Liveness and Prometheus metrics.

---

## Worker protocol

A worker is just a loop over the HTTP API, so it works in any language:

1. `claim` a job (long-poll).
2. Start a background heartbeat that calls `renew` at ~1/3 of the lease interval.
3. Run the handler.
4. On success → `ack`. On exception → `fail`.
5. Stop the heartbeat. Repeat.

The SDKs implement this loop for you, but the protocol is plain HTTP — anyone can write a worker in any language without an official SDK. That's what makes Relay language-agnostic.

---

## SDK usage

### Python
```python
from relay import Worker

worker = Worker('localhost:8080', 'YOUR_KEY')

@worker.handle('emails', lease=30)
def send_email(payload):
    send(payload['to'])

worker.run(concurrency=4)
```

### Node
```js
import { Worker } from 'relay-sdk';

const worker = new Worker('localhost:8080', 'YOUR_KEY');

worker.handle('emails', async (payload) => {
  await sendEmail(payload.to);
});

worker.run({ concurrency: 4 });
```

### Go
```go
w := relay.NewWorker("localhost:8080", "YOUR_KEY")

w.Handle("emails", func(p relay.Payload) error {
    return sendEmail(p["to"].(string))
})

w.Run(relay.Concurrency(4))
```

Each SDK handles claiming, heartbeating, ack/fail, and concurrency. Returning an error (or panicking) marks the job failed; returning nil acks it.

---

## Self-hosting

Two processes: the Go binary and Redis.

### Docker Compose
```yaml
services:
  relay:
    image: relay:latest
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

### From source
```bash
git clone https://github.com/YOURNAME/relay
cd relay
go build -o relay ./cmd/relay
REDIS_ADDR=localhost:6379 ./relay
```

### Deployed (AWS)
The reference deployment runs the container on AWS ECS Fargate with Redis on ElastiCache (or a small self-managed Redis container to stay free). A GitHub Actions workflow builds, pushes to ECR, and rolls out on merge to `main`. See `deploy/`.

---

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP port |
| `REDIS_ADDR` | `localhost:6379` | Redis host:port |
| `REDIS_PASSWORD` | _(empty)_ | Redis auth, if set |
| `DEFAULT_LEASE_SECONDS` | `30` | Lease length if a worker doesn't specify |
| `REAPER_INTERVAL_MS` | `1000` | How often the reaper scans for expired leases |
| `MAX_RETRIES` | `5` | Default retries before dead-letter |
| `BACKOFF_BASE_MS` | `1000` | Base for exponential retry backoff |
| `IDEMPOTENCY_WINDOW_SECONDS` | `3600` | How long acked job IDs are remembered for dedup |
| `MAX_PAYLOAD_BYTES` | `65536` | Max job payload size |

---

## Observability

A job queue is useless if you can't see into it, so observability is first-class:

- **Dashboard** — live queue depths (pending/leased/retry/dead), throughput, failure rate, and a dead-letter browser with one-click requeue.
- **`/metrics`** — Prometheus: enqueue rate, claim rate, ack rate, fail rate, reaper re-queues, queue depths, claim latency.
- **Grafana** — a ready-made dashboard JSON in `deploy/grafana/`.

This is also where Relay connects to the broader "lightweight developer infra" theme — it's observable by design, not as an afterthought.

---

## Performance

Relay is built to do a lot on a small box. The atomic Redis operations and the simple lease model keep per-job overhead low.

Benchmarked on [INSTANCE TYPE — fill in], single instance:

| Metric | Result |
|--------|--------|
| Sustained enqueue throughput | [FILL IN] jobs/sec |
| Sustained claim+ack throughput | [FILL IN] jobs/sec |
| p99 claim latency | [FILL IN] ms |
| Time to re-queue after worker death | [FILL IN] ms (≈ reaper interval + remaining lease) |
| Memory footprint | [FILL IN] MB |

Reproduce with the harness in `loadtest/` — it spins up N workers, kills a fraction mid-job, and verifies zero jobs are lost.

> Fill every bracket with real measured numbers before publishing. The "kill workers, verify nothing is lost" test is your strongest demo and your best interview story — make it real and record the result.

---

## Roadmap

Deliberately short. Feature creep is the main risk.

- [ ] Core: enqueue, claim, lease, ack, fail, retry, dead-letter
- [ ] Reaper + crash-recovery correctness
- [ ] Python / Node / Go SDKs
- [ ] Dashboard + Prometheus metrics
- [ ] Scheduled / delayed jobs (run-at timestamp)
- [ ] Priority queues

Intentionally **not** planned: pub/sub fan-out, streaming, a broker cluster, exactly-once guarantees. Those break the "one light binary" promise.

---

## Contributing

Issues and PRs welcome. The project values staying small — changes that add a backing service or grow the operational footprint will likely be declined by design. Great first contributions: SDKs for more languages, dashboard polish, docs, and edge cases in the load/chaos test harness.

---

## License

MIT. Self-host it, fork it, do what you want.