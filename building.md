# Building Cogs By Hand — A Step-by-Step Guide

This is a literal, ordered checklist for building Cogs yourself, without AI writing the code for you. Each step says what to build, what to read before building it, and how to know you actually understand it before moving on.

**Rule for the whole project:** for every step, try it yourself for 20–30 minutes first. Only then read the linked doc. Only after reading the doc and trying again should you look at any example online — and if you do, retype it by hand and change something, never paste. After finishing each step, write 2–3 sentences in your own words explaining what you built and why. If you can't write that explanation, you don't understand it yet — stop and re-read before moving on.

**References to keep open the whole time:**
- Go standard library docs — https://pkg.go.dev/std
- Redis command reference — https://redis.io/docs/latest/commands/
- go-redis client docs — https://redis.uptrace.dev/guide/
- Go by Example (for syntax only, not architecture) — https://gobyexample.com/

---

## Phase 0 — Prerequisites (before touching the real project)

### Step 0.1 — Go syntax refresher
Write three throwaway scripts: one with structs and methods, one with goroutines and channels, one with error handling (`if err != nil` patterns, custom errors). Don't move on until you're not pausing to remember syntax.

### Step 0.2 — A bare HTTP server
Build a standalone `main.go` with `net/http`: a `ServeMux`, two routes, one that reads a JSON body and one that writes a JSON response. Read: `net/http` and `encoding/json` docs. Done when: you can explain what `http.ResponseWriter` and `*http.Request` are without looking it up.

### Step 0.3 — Learn Redis commands in `redis-cli`, not in Go
Open `redis-cli` directly (no code yet). Run and observe the output of each, reading its doc page first:
- `LPUSH`, `RPUSH`, `LRANGE`, `BRPOP`, `LMOVE`
- `ZADD`, `ZRANGEBYSCORE`, `ZPOPMIN`, `ZREM`
- `HSET`, `HGETALL`, `HDEL`
- `SET key val EX 30 NX`, `TTL`, `EXPIRE`
- `MULTI` / `EXEC` (transactions)
- `EVAL` with a one-line Lua script (just to see it run)

Done when: for each command, you can predict its output before pressing enter.

### Step 0.4 — Connect Go to Redis
Install `go-redis`. Write a throwaway program: connect, `LPUSH` a value, `BRPOP` it back, print it. Read: go-redis "getting started" guide. Done when: it runs and you understand every line, including the context (`ctx`) argument.

### Step 0.5 — Docker Compose basics
Write a `docker-compose.yml` with just a Redis service. Run `docker compose up`, connect to it from your Step 0.4 program. Read: Docker Compose docs, "compose file reference" for the syntax you used. Done when: you can explain what the `ports:` and `image:` fields do.

---

## Phase 1 — The thinnest working queue

**Goal:** a job goes in, a worker pulls it out, acks it. Two workers never grab the same job. No retries, no leases yet.

### Step 1.1 — Design on paper first
Before any code, write down (in your Obsidian vault, not in code):
- What fields does a job have? (id, queue, payload, created_at)
- Where do pending jobs live in Redis? (a list, key named per queue, e.g. `queue:emails:pending`)
- What Redis command atomically removes one job so two workers can't both get it?

Look up `BRPOP` (blocking pop) and `LMOVE` and decide which fits "atomically take one job." Write your reasoning down.

### Step 1.2 — Enqueue endpoint
Build `POST /v1/jobs`:
- Parse JSON body into a `Job` struct.
- Generate an ID. Read the `crypto/rand` or a UUID package's docs; understand how the ID is generated, don't just call a function blindly.
- `LPUSH` the serialized job onto `queue:{name}:pending`.
- Return `201` with the job ID.

Done when: you can `curl` an enqueue and see the job appear in Redis (`LRANGE queue:emails:pending 0 -1` in `redis-cli`).

### Step 1.3 — Claim endpoint
Build `POST /v1/jobs/claim`:
- Accepts a list of queue names.
- Uses `BRPOP` (or `LMOVE` if you decided that's more correct in 1.1) to atomically take one job off the list.
- Returns the job JSON, or `204` if nothing available after a short timeout.

Read the `BRPOP` doc closely for what "blocking" actually means and what happens with multiple clients calling it at once — this is the answer to "how do two workers never collide."

### Step 1.4 — Ack endpoint
Build `POST /v1/jobs/{id}/ack`. For now this can be a no-op or just a log line — the job is already off the pending list once claimed, so there's nothing to remove yet. (This will matter once you add leases in Phase 2.)

### Step 1.5 — Prove it with two workers
Write a throwaway worker script (Python or a second Go program) that loops: claim, print, ack. Run two copies of it at once against a queue you've filled with 20 jobs. Confirm every job is processed exactly once, by neither worker getting a duplicate.

**Write your understanding note before moving on:** how does Redis guarantee the atomic claim? What would happen if you used `LRANGE` + `LREM` instead of `BRPOP`/`LMOVE`? (Hint: race condition — figure out why and write it down.)

---

## Phase 2 — Leases and crash recovery (the core of the project)

**Goal:** a job whose worker dies gets automatically re-queued. This is the distributed-systems heart — go slowly, no AI for any of this phase.

### Step 2.1 — Understand the lease concept on paper
Before code, write the state machine for a job: `pending → leased → (acked | failed → retry | dead)`. For the leased state, write down: what data do you need to know a lease has expired? (the job ID, which worker holds it, and a deadline timestamp).

### Step 2.2 — Redesign claim to create a lease, atomically
This is the hardest single step in the project. The problem: claiming a job (removing it from pending) and creating its lease record (so the reaper can find it) need to happen as one atomic operation, or a crash between the two steps could lose the job.

Read about Redis `MULTI`/`EXEC` transactions, and separately read about Lua scripting via `EVAL` (Redis guarantees a Lua script runs atomically). Decide which fits better — write down your reasoning. (Most production queues like this use a Lua script for exactly this reason: one atomic claim-and-lease operation.)

Implement it: the claim operation should, in one atomic step, (a) pop a job from pending, and (b) write a lease record — a Redis key like `lease:{job_id}` with a value containing the worker ID, with a `TTL` matching the lease duration set via `EX` on the `SET` command.

Done when: you can explain, out loud, why doing this in two separate Redis calls instead of one atomic operation would be a real bug, with a concrete scenario.

### Step 2.3 — Renew (heartbeat) endpoint
Build `POST /v1/jobs/{id}/renew`. It should extend the TTL on the `lease:{job_id}` key (read `EXPIRE` or re-`SET` with `EX`/`KEEPTTL` docs to pick the right one). Returns the new deadline.

### Step 2.4 — Real ack endpoint
Now that leases exist, ack needs to delete the lease key (`DEL lease:{job_id}`) so the job is truly done and the reaper won't touch it.

### Step 2.5 — The reaper
This is a background goroutine, not an HTTP endpoint. Read about Go's `time.Ticker` for running something on an interval, and about goroutines + a `context.Context` for clean shutdown.

The reaper's job: periodically find leases that have expired and re-queue those jobs. The tricky part — Redis TTLs delete keys automatically, so an expired lease key simply *disappears*, you can't query "give me all expired leases" directly. You need to design around this. Two real approaches, read about both and pick one, writing down why:
- (a) Keep a separate Redis sorted set of `job_id → lease_deadline` (using `ZADD` with the deadline as the score) alongside the TTL key. The reaper runs `ZRANGEBYSCORE` for entries with deadline < now, and for each, re-pushes the job to pending and removes the entry.
- (b) Use Redis keyspace notifications to get an event when a key expires, and react to that event.

(a) is simpler and more controllable — recommended for your first version.

Done when: you can kill a worker process mid-job (literally `kill -9` it) and watch, in `redis-cli` or your logs, the job get re-queued by the reaper within roughly one lease interval.

### Step 2.6 — Retry with backoff
On `fail`, instead of immediately re-queuing, compute a backoff delay (read about exponential backoff — e.g., `base * 2^attempt`) and schedule it using the same sorted-set pattern: `ZADD retry:{queue} next_attempt_time job_id`. A second small scheduler loop (another ticker) moves jobs from this sorted set back to pending once their time has passed.

### Step 2.7 — Dead-letter queue
Track an attempt count on the job. When `fail` is called and attempts exceed `MAX_RETRIES`, push the job to a `dead:{queue}` list instead of retrying. Build `GET /v1/dead` to list them.

### Step 2.8 — Idempotency keys
On ack, store the job's idempotency key in a Redis set with a TTL (`SADD` + separate `EXPIRE`, or a sorted set with timestamp scores you periodically trim). On claim or ack, check if the key's already been seen and skip/flag duplicates.

### Step 2.9 — The chaos test (build this, keep it forever)
Write a script (Go test or a separate program) that:
1. Enqueues N jobs.
2. Starts several worker processes.
3. Randomly kills a fraction of them mid-job (`os/exec` to spawn, then `Process.Kill()` partway through).
4. Waits, then asserts every single job eventually reaches `acked` — none stuck, none lost.

This is your proof of correctness, your demo GIF, and your interview story all in one. Don't skip it, don't fake it.

**Write your understanding note:** explain the full lease lifecycle out loud to yourself, end to end, including exactly what happens if a worker dies one second after claiming versus one second before finishing.

---

## Phase 3 — Scheduler (cron / recurring jobs)

**Goal:** jobs that run on a schedule, reliably, even across restarts.

### Step 3.1 — Cron expression parsing
Read about cron syntax (minute, hour, day, month, weekday). Find a small, well-documented cron-parsing library for Go and read its docs to understand how it computes "next run time" from an expression — don't just call it as a black box, read how it works.

### Step 3.2 — Schedule storage
Store each schedule (queue, cron expression, payload, next-run-time) in Redis — a sorted set `schedules` scored by next-run-time works the same way your retry queue does.

### Step 3.3 — Scheduler loop
Another ticker-based goroutine: every interval, check the `schedules` sorted set for entries due now or earlier, enqueue a real job for each, and recompute + update their next-run-time using the cron library.

Done when: you register a schedule for "every minute," restart the whole server, and confirm jobs keep firing on schedule without gaps or duplicates.

---

## Phase 4 — SDKs and ergonomics

### Step 4.1 — Document the raw protocol
Write down the exact HTTP calls a worker makes, in order, with example payloads. This is your contract — anyone should be able to implement a worker from this doc alone.

### Step 4.2 — Python SDK
Build a small Python class that wraps: claim (loop with a short sleep or long-poll), spawn a background thread that calls renew on an interval, run the user's handler function, call ack or fail based on whether it raised an exception. Read Python's `threading` docs for the heartbeat thread.

### Step 4.3 — Node SDK
Same shape, using `setInterval` for the heartbeat and async/await for the claim loop. Read Node's `http`/`fetch` and `setInterval` docs.

### Step 4.4 — Time the quickstart
Starting from nothing, time yourself: `docker run`, enqueue a job, write and run a five-line worker. If it's not under two minutes, find the friction and remove it.

---

## Phase 5 — Deploy and observe on AWS

### Step 5.1 — Dockerize properly
Write a multi-stage `Dockerfile` (build stage compiles Go, final stage is a tiny base image with just the binary). Read Docker's multi-stage build docs — understand why this makes the image smaller and more secure.

### Step 5.2 — Push to ECR
Read AWS's ECR docs for the login/tag/push flow. Do it manually by hand once via the CLI before automating it — you want to understand each step before a pipeline hides it.

### Step 5.3 — ECS Fargate deployment
Read the ECS Fargate getting-started docs. Manually create a task definition and service once through the console or CLI so you understand what a task, a service, and a cluster are, before writing any Infrastructure-as-code.

### Step 5.4 — Redis on AWS
Decide: ElastiCache (managed, costs money beyond free tier limits) or a small self-managed Redis container in the same setup (free, you manage it). Read the tradeoffs, pick one, write down why.

### Step 5.5 — GitHub Actions CI/CD
Write a workflow YAML by hand: on push to main, build the Docker image, push to ECR, update the ECS service. Read GitHub Actions docs for the AWS-specific actions you use — understand each step's inputs/outputs rather than copying a template blindly.

### Step 5.6 — Metrics and dashboard
Add a `/metrics` endpoint using the `prometheus/client_golang` library — read its docs for how counters, gauges, and histograms differ, and pick the right type for each metric (e.g., a histogram for claim latency, a gauge for queue depth). Wire up Grafana to visualize them. Build the React dashboard last, calling your own `/v1/queues/{name}/stats` endpoint.

### Step 5.7 — Load test
Use a tool like `k6` or `wrk` (read their docs for basic usage) to hammer the enqueue and claim endpoints. Record real throughput and latency numbers. Run your Phase 2.9 chaos test against the deployed version too, not just locally.

---

## Phase 6 — Launch

### Step 6.1 — Record the demo
Screen-record: jobs processing normally, then you `kill -9` a worker mid-job, then the job reappears and completes on another worker. Convert to a GIF.

### Step 6.2 — Write the launch post
Using your own words (not AI-generated), describe: the problem you personally hit, what Cogs does, how the leases work in a sentence or two, that it's free and self-hostable, link the repo.

### Step 6.3 — Post and engage
Post to r/selfhosted and r/golang. Reply to every comment within the first two hours. If someone requests a reasonable feature, build it and reply with what you shipped.

---

## After each phase: the note you write yourself

For every phase, before moving to the next one, write in Obsidian:
- What does this phase do, in your own words?
- What was the hardest decision, and what did you decide and why?
- What would break this if you removed it?
- What would you ask yourself in an interview about this phase, and what's your answer?

If you can't answer those four questions for a phase, you're not done with it yet — even if the code runs.