# Cogs

**Reliable background jobs and cron for self-hosted apps. One Go binary plus Redis. Define jobs in any language, get automatic retries, crash recovery, and a dashboard. Jobs survive restarts and deploys.**

> The thing to reach for when a cron-job-plus-database hack loses your work on crash, but Celery and RabbitMQ are too much to operate. One binary, two-minute setup, free, self-hostable.

---

## The problem

Every small service needs background work — process an upload, send an email, sync data nightly — and the options at this scale are bad. Hand-rolled cron + a DB table silently loses jobs when a box restarts or a deploy lands mid-run. Celery is heavy and Python-only. RabbitMQ/Kafka are real infrastructure. Cloud queues cost money and lock you in.

Cogs is the light, crash-safe option: define jobs once, run workers in any language, and never lose a job to a crash or a deploy.

## What it does

- **Background jobs** — enqueue work, a worker runs it, retries with backoff on failure, dead-letters after max attempts.
- **Scheduled & recurring jobs** — cron expressions for nightly/hourly tasks, run reliably even across restarts.
- **Crash recovery** — a job whose worker dies is automatically re-queued. Nothing is lost.
- **Any language** — workers talk a simple HTTP protocol; SDKs for Python, Node, Go.
- **Observable** — dashboard of every run: pending, running, succeeded, failed, retrying, dead.

## What it's not

Not a Kafka/RabbitMQ replacement at scale, not pub/sub, not exactly-once (it's at-least-once + idempotency keys — the honest guarantee). The value is being light and crash-safe for small-to-medium workloads.

---

## Core idea: leases

A worker doesn't remove a job, it takes a **lease** with a deadline. It **renews** the lease (heartbeat) while working and **acks** when done. If the worker dies, the lease expires, a **reaper** notices and re-queues the job. Same mechanism as SQS visibility timeouts and Kubernetes leases — crash safety without consensus.

## Architecture

```
  enqueue (any lang) ──► Enqueue API ──┐
  cron schedules ──────► Scheduler ────┤
                                       ▼
                                    Redis
                          pending · leased · retry
                          schedules · dead
                                       ▲
        claim/renew/ack/fail   ┌───────┼────────┐
                               │       │        │
                          Worker A  Worker B  Reaper
                          (python)  (node)   (goroutine:
                                              re-queues
                                              dead workers)

  Cogs server (Go): Enqueue + Claim/lease API + Scheduler + Reaper
  + /metrics (Prometheus) + dashboard (React).
  Deployed on AWS ECS Fargate, CI/CD via GitHub Actions.
```

| Component | Tech | Role |
|-----------|------|------|
| Enqueue / lease API | Go (`net/http`) | accept jobs; atomic claim, renew, ack, fail |
| Scheduler | Go + Redis sorted set | enqueue cron/recurring jobs at their due time |
| Reaper | Go goroutine | detect expired leases, re-queue dead jobs |
| Storage | Redis | pending, leased, retry, schedules, dead-letter |
| SDKs | Python / Node / Go | worker loop: claim, heartbeat, ack/fail |
| Dashboard | React | live run history and queue state |

## Delivery semantics

At-least-once. A job can rarely run twice (worker finishes then dies before ack). True exactly-once is impossible under failure — Cogs provides idempotency keys to dedup. Design handlers to be idempotent.

---

## Quickstart

```bash
# 1. run Cogs
docker run -p 8080:8080 cogs

# 2. enqueue a job (any language, just HTTP)
curl -X POST localhost:8080/v1/jobs \
  -H "Authorization: Bearer KEY" \
  -d '{"queue":"emails","payload":{"to":"a@b.com"}}'
```

```python
# 3. run a worker
from cogs import Worker
w = Worker("localhost:8080", "KEY")

@w.handle("emails")
def send(payload):
    send_email(payload["to"])     # retried automatically on failure

w.run()
```

Schedule a recurring job:
```bash
curl -X POST localhost:8080/v1/schedules \
  -H "Authorization: Bearer KEY" \
  -d '{"queue":"cleanup","cron":"0 3 * * *","payload":{}}'   # 3am daily
```

## API (core)

| Endpoint | Purpose |
|----------|---------|
| `POST /v1/jobs` | enqueue a job |
| `POST /v1/jobs/claim` | worker claims next job (long-poll), takes a lease |
| `POST /v1/jobs/{id}/renew` | extend lease (heartbeat) |
| `POST /v1/jobs/{id}/ack` | mark done |
| `POST /v1/jobs/{id}/fail` | report failure → retry or dead-letter |
| `POST /v1/schedules` | register a cron/recurring job |
| `GET /v1/queues/{name}/stats` | depths + throughput |
| `GET /healthz` · `GET /metrics` | liveness · Prometheus |

## Self-hosting

```yaml
# docker-compose.yml
services:
  cogs:
    image: cogs:latest
    ports: ["8080:8080"]
    environment: [REDIS_ADDR=redis:6379]
    depends_on: [redis]
  redis:
    image: redis:7-alpine
```

AWS: container on ECS Fargate, Redis on ElastiCache (or self-managed to stay free-tier), CI/CD via GitHub Actions (build → ECR → roll out). See `deploy/`.

## Config (env)

`PORT` · `REDIS_ADDR` · `REDIS_PASSWORD` · `DEFAULT_LEASE_SECONDS` (30) · `REAPER_INTERVAL_MS` (1000) · `MAX_RETRIES` (5) · `BACKOFF_BASE_MS` (1000) · `IDEMPOTENCY_WINDOW_SECONDS` (3600)

## Performance

Single instance on [INSTANCE — fill in]:

| Metric | Result |
|--------|--------|
| Throughput (claim+ack) | [FILL IN] jobs/sec |
| p99 claim latency | [FILL IN] ms |
| Re-queue after worker death | [FILL IN] ms |

Reproduce with `loadtest/` — spawns workers, kills a third mid-job, verifies zero job loss. *(Fill brackets with real measured numbers only.)*

## Build order

0. **Validate** — confirm the pain on r/selfhosted, r/golang; write the pitch.
1. **Thin queue** — enqueue → claim → ack, two workers, no double-processing.
2. **Leases + reaper** — crash recovery, retries, backoff, dead-letter, idempotency. *Build the chaos test here. This is the core.*
3. **Scheduler** — cron/recurring jobs (the feature that makes it a product, not a primitive).
4. **SDKs** — Python, Node, Go worker loops; two-minute quickstart.
5. **Deploy + observe** — AWS ECS, CI/CD, dashboard, Prometheus/Grafana.
6. **Launch** — free, MIT, GIF of `kill -9` a worker and the job surviving.

## Scope (intentionally not building)

Pub/sub fan-out · broker clusters · exactly-once · SQL querying. These break the one-light-binary promise. Lightness is the product.

## License

MIT.