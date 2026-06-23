# CLAUDE.md — Relay

Context file for building **Relay**, a lightweight self-hostable distributed job queue. Read this first before working on the project. It defines what we're building, why, the build order, and the rules for how to work.

---

## What we're building

A lightweight, self-hostable job queue. One Go binary plus Redis. Workers in any language pull jobs over a simple HTTP protocol. Jobs survive worker crashes via leases.

**One-line pitch:** the simple thing to reach for when Celery is too Python-specific, RabbitMQ is too much to operate, and SQS means locking into AWS.

**Stack:** Go (server), Redis (state), Python/Node/Go (worker SDKs), React (dashboard), Docker, AWS ECS Fargate, GitHub Actions, Prometheus + Grafana.

---

## Why it exists (keep this in mind for every decision)

Small projects need background jobs but the existing options don't fit: Celery is heavy and Python-only, RabbitMQ/Kafka are real infrastructure, cloud queues cost money and lock you in. So people hand-roll fragile DB-table-plus-cron hacks that silently lose jobs on crash. Relay is the light, language-agnostic, crash-safe option for that scale.

**The two non-negotiable values:**
1. **Lightness** — one binary + Redis, two-minute setup. The moment it needs a second backing service, it loses its only advantage.
2. **Crash safety** — a job whose worker dies is automatically re-queued. Nothing is lost.

If a feature threatens either of these, it's probably out of scope.

---

## The core mechanism: leases

Everything rests on this. Understand it before writing code.

When a worker takes a job it doesn't remove it — it takes a **lease** with a deadline. While leased, no other worker can touch the job. The worker **renews** the lease periodically (heartbeat) while working. On finish it **acks**, and the job is gone.

If the worker dies, it stops renewing → the lease expires → a **reaper** loop notices and puts the job back on the queue. Same idea as SQS visibility timeouts and Kubernetes leases.

`lease → renew → expire → re-queue`. That single loop is how we survive crashes without consensus.

---

## Delivery semantics (state this honestly, never oversell)

Relay is **at-least-once**. A job can rarely run more than once (worker finishes then dies before acking). True exactly-once is impossible under failure — what real systems offer is at-least-once + idempotency. We provide idempotency keys to dedup the common case. Never claim exactly-once.

---

## Build order

Build in this order. Do **not** jump ahead — each phase depends on the last. Ship the thinnest working thing first, add the distributed depth second, then ergonomics, deploy, launch.

### Phase 0 — Validate (2–3 days, no code)
- Search r/selfhosted, r/golang, r/django, HN for the pain ("Celery too heavy", "simple job queue", "RabbitMQ overkill"). Collect 8–10 real complaints for launch copy.
- Post a soft probe describing the idea, gauge interest, collect early followers.
- Finalize the README pitch and the "kill a worker, job survives" demo concept.

### Phase 1 — Thinnest working queue (Week 1)
Goal: a job flows enqueue → claim → ack with two workers, no double-processing. No failure handling yet.
- `POST /v1/jobs` — enqueue to a Redis pending list.
- `POST /v1/jobs/claim` — atomically pop a job for one worker.
- `POST /v1/jobs/{id}/ack` — remove the job.
- Throwaway worker script proving the loop; run two workers, confirm neither double-processes.
- Redis: learn `LPUSH`/`BRPOP` and why atomic claim matters. **Understand-mode, no AI for the Redis concurrency part.**

### Phase 2 — Leases + crash recovery (Weeks 2–3) ← THE POINT OF THE PROJECT
Goal: jobs survive worker death. This is the distributed-systems core and the interview story. Go slow, understand-mode throughout.
- Convert claim into a **lease**: atomically move pending → leased with a deadline. Use a Lua script or `MULTI` so two workers can never claim the same job.
- **Heartbeat**: `POST /v1/jobs/{id}/renew` extends the lease; SDK calls it on a timer at ~1/3 the lease interval.
- **Reaper** goroutine: scan for expired leases, move those jobs back to pending.
- **Retry** with exponential backoff (Redis sorted set keyed by next-attempt time).
- **Dead-letter** queue after max retries.
- **Idempotency keys**: remember recently-acked IDs to dedup rare double-delivery.
- **Build the chaos test here**: spawn workers, kill a third mid-job, assert zero jobs lost. This test is the demo AND the interview story — make it real.

### Phase 3 — SDKs + ergonomics (Week 4)
Goal: a real user adopts it in two minutes.
- Worker SDKs hiding the claim/heartbeat/ack loop: Python and Node first, Go third.
- `@worker.handle('queue')` style — claim, heartbeat, ack/fail, concurrency all handled. Returning an error/panic = fail; returning nil = ack.
- Make quickstart genuinely two minutes: `docker run`, enqueue with curl, run a worker. Time it cold.
- Document the raw HTTP protocol so anyone can write a worker without an SDK (the "any language" promise).

### Phase 4 — Deploy + observe on AWS (Weeks 5–6)
Goal: live URL + self-observability.
- Containerize, deploy to AWS ECS Fargate. Redis via ElastiCache or small self-managed container to stay free-tier.
- GitHub Actions CI/CD: push → build → push to ECR → roll out. Health checks on `/healthz`.
- React dashboard: live queue depths (pending/leased/retry/dead), throughput, failure rate, dead-letter browser with one-click requeue.
- `/metrics` for Prometheus + a ready Grafana dashboard.
- Load test on AWS; record real throughput and re-queue-after-death timing.

### Phase 5 — Launch (Week 7)
Goal: real users + real numbers = the interview story.
- Free, MIT-licensed, self-hostable.
- Launch GIF: jobs processing, `kill -9` a worker, jobs re-queue and finish elsewhere. This sells the value prop in five seconds.
- Post format: "I built X because Celery was too heavy / RabbitMQ overkill. One binary, any language, survives crashes. Here's how the leases work. It's free. Roast it."
- Targets: r/selfhosted, r/golang, r/django, r/node, HN "Show HN".
- Reply to every comment in the first 2 hours; ship a requested feature live and post about it.

---

## Scope: explicitly NOT building

Declining these is a deliberate design decision (and a good interview talking point about judgment):
- Pub/sub fan-out or general event streaming — this is a work queue.
- A broker cluster or multi-node coordination beyond Redis — breaks the "one light binary" promise.
- Exactly-once delivery — impossible; we do at-least-once + idempotency.
- SQL querying, session features, anything that adds a second backing service.

Future-OK (only after core is solid): scheduled/delayed jobs, priority queues, more SDK languages.

---

## How to work on this (rules)

- **Understand-mode vs execute-mode.** For anything new or any failure logic (all of Phase 2, Redis concurrency, the reaper), work without AolI assistance first — read docs, reason it out, then implement. Use AI only to scaffold things I already understand and could explain line by line. If I can't explain a piece of code, stop and understand it before moving on.
- **After each work session**, write a short note: what I built, how it works, why each decision, what would break it. This is the interview-prep byproduct.
- **The chaos test is sacred.** It verifies correctness, it's the launch demo, it's the interview story. Keep it green.
- **Protect the scope.** Default to "no" on features that add weight. Lightness is the product.
- **Round, validate, handle errors** — this is infrastructure other people will self-host; correctness and clear failure behavior matter more than features.
- **Real numbers only.** Never put invented benchmarks in the README or anywhere else. Measure, then write.

---

## Resume bullets this produces (the target)

```
Built a self-hostable distributed job queue in Go — lease-based job ownership,
heartbeat failure detection with automatic re-queue, idempotent at-least-once
delivery, and dead-letter handling. [N] developers using it, [M] jobs/day.

Deployed to AWS ECS with autoscaling and CI/CD via GitHub Actions; built a live
observability dashboard (React, Prometheus, Grafana) and load-tested to
[X] jobs/sec with verified zero job loss under random worker termination.
```

Everything in those bullets must be literally true and defensible by the end. Build toward making them true, not toward padding them.