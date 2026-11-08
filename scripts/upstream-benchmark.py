#!/usr/bin/env python3
"""Replay captured optimizer requests against upstream and optima.

Reads the JSONL request dump written by OPTIMIZER_DUMP_REQUESTS, POSTs each
request to two or more services, and compares status, objective and latency.

Subcommands:
  replay  Replay every captured request against both services, one full pass per
          service so the two never compete for CPU. Writes one result line per
          request.
  report  Summarise a replay file: errors, statuses, quality, latency.
  bench   Sequential, uncontended latency and quality comparison on an evenly
          spread sample. Alternate request order to cancel warm-up bias.

Examples:
  scripts/upstream-benchmark.py replay --source requests.jsonl \
      --official http://localhost:17050 --ours http://localhost:27050 \
      --out /tmp/results.jsonl
  scripts/upstream-benchmark.py report --results /tmp/results.jsonl
  scripts/upstream-benchmark.py bench --source requests.jsonl \
      --endpoint official=http://localhost:17050 \
      --endpoint ours=http://localhost:27050 --n 400
"""
import argparse
import json
import statistics
import time
import urllib.request
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

PATH = "/optimize/charge-schedule"
TIMEOUT = 90


def post(base, payload, timeout=TIMEOUT):
    req = urllib.request.Request(
        base.rstrip("/") + PATH,
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    started = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = json.loads(resp.read())
        return {
            "elapsed": time.monotonic() - started,
            "status": body.get("status"),
            "objective": body.get("objective_value"),
            "error": None,
        }
    except Exception as exc:  # noqa: BLE001 - the harness records everything
        return {
            "elapsed": time.monotonic() - started,
            "status": None,
            "objective": None,
            "error": f"{type(exc).__name__}: {exc}",
        }


def cmd_replay(args):
    lines = Path(args.source).read_text().splitlines()
    payloads = [json.dumps(json.loads(line)["request"]).encode() for line in lines]
    results = []
    for line in lines:
        captured = json.loads(line)
        results.append(
            {
                "time": captured.get("time"),
                "client": captured.get("client"),
                "recorded": {
                    "status": captured.get("status"),
                    "objective": (captured.get("response") or {}).get("objective_value"),
                    "elapsed": captured.get("elapsed"),
                },
            }
        )

    # One full pass per service, never both at once: overlapping solves on the
    # same host would contaminate the latency comparison.
    for who in ("official", "ours"):
        url = getattr(args, who)
        print(f"pass {who}: {len(payloads)} requests, {args.workers} in flight", flush=True)

        def solve_one(item):
            idx, payload = item
            return idx, post(url, payload)

        with ThreadPoolExecutor(args.workers) as pool:
            for n, (idx, res) in enumerate(pool.map(solve_one, enumerate(payloads)), 1):
                results[idx][who] = res
                if n % 100 == 0:
                    print(f"{who} {n}/{len(payloads)}", flush=True)

    with Path(args.out).open("w") as fh:
        for res in results:
            fh.write(json.dumps(res) + "\n")
    print("done", flush=True)


def percentile(xs, q):
    return xs[min(len(xs) - 1, int(len(xs) * q))]


def cmd_report(args):
    rows = [json.loads(line) for line in open(args.results)]
    print(f"rows: {len(rows)}")

    print("\n## errors")
    for who in ("official", "ours"):
        errs = Counter(r[who]["error"] for r in rows if r[who]["error"])
        print(who, dict(errs) or "none")

    print("\n## statuses")
    for who in ("official", "ours"):
        print(f"{who:8s}", dict(Counter(r[who]["status"] for r in rows)))
    mismatches = [r for r in rows if r["official"]["status"] != r["ours"]["status"]]
    print("status mismatches:", len(mismatches))

    print("\n## latency (seconds)")
    for who in ("official", "ours"):
        xs = sorted(r[who]["elapsed"] for r in rows if not r[who]["error"])
        if xs:
            print(
                f"{who:8s} mean={statistics.mean(xs):.4f} p50={percentile(xs, .5):.4f} "
                f"p90={percentile(xs, .9):.4f} p99={percentile(xs, .99):.4f} "
                f"max={xs[-1]:.4f} sum={sum(xs):.1f}"
            )

    print("\n## quality (higher objective is better)")
    comparable = [
        r for r in rows if r["official"]["objective"] is not None and r["ours"]["objective"] is not None
    ]
    deltas = [r["ours"]["objective"] - r["official"]["objective"] for r in comparable]
    better = sum(d > 1e-5 for d in deltas)
    worse = sum(d < -1e-5 for d in deltas)
    print(f"comparable {len(comparable)}: ours better {better}, worse {worse}, equal within 1e-5 {len(deltas) - better - worse}")
    if deltas:
        print(
            f"delta sum {sum(deltas):+.6f} mean {statistics.mean(deltas):+.3e} "
            f"min {min(deltas):+.6f} max {max(deltas):+.6f}"
        )


def cmd_bench(args):
    lines = Path(args.source).read_text().splitlines()
    step = max(1, len(lines) // args.n)
    sample = lines[::step][: args.n]
    times = {name: [] for name, _ in args.endpoint}
    objectives = {name: [] for name, _ in args.endpoint}
    for i, line in enumerate(sample):
        payload = json.dumps(json.loads(line)["request"]).encode()
        order = list(args.endpoint)
        if i % 2:
            order.reverse()
        for name, url in order:
            res = post(url, payload)
            times[name].append(res["elapsed"])
            objectives[name].append(res["objective"])
        if (i + 1) % 50 == 0:
            print(i + 1, flush=True)

    print(f"\nsample {len(sample)} sequential requests")
    for name, _ in args.endpoint:
        xs = sorted(times[name])
        print(
            f"{name:12s} mean={statistics.mean(xs):.4f} p50={percentile(xs, .5):.4f} "
            f"p90={percentile(xs, .9):.4f} p99={percentile(xs, .99):.4f} max={xs[-1]:.4f}"
        )

    baseline = dict(args.endpoint)[args.baseline]
    for name, url in args.endpoint:
        if url == baseline:
            continue
        ds = [
            a - b
            for a, b in zip(objectives[name], objectives[args.baseline])
            if a is not None and b is not None
        ]
        print(
            f"{name} vs {args.baseline}: better={sum(d > 1e-5 for d in ds)} "
            f"worse={sum(d < -1e-5 for d in ds)} equal={sum(abs(d) <= 1e-5 for d in ds)} "
            f"sum_delta={sum(ds):+.6f} min={min(ds):+.6f} max={max(ds):+.6f}"
        )


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(required=True)

    replay = sub.add_parser("replay", help="replay a captured request dump against both services")
    replay.add_argument("--source", default="requests.jsonl")
    replay.add_argument("--official", default="http://localhost:17050")
    replay.add_argument("--ours", default="http://localhost:27050")
    replay.add_argument("--out", default="/tmp/upstream-replay.jsonl")
    replay.add_argument("--workers", type=int, default=4)
    replay.set_defaults(func=cmd_replay)

    report = sub.add_parser("report", help="summarise a replay result file")
    report.add_argument("--results", default="/tmp/upstream-replay.jsonl")
    report.set_defaults(func=cmd_report)

    bench = sub.add_parser("bench", help="sequential latency comparison on a spread sample")
    bench.add_argument("--source", default="requests.jsonl")
    bench.add_argument("--endpoint", action="append", required=True, metavar="NAME=URL")
    bench.add_argument("--baseline", help="endpoint name to compare quality against (default: first)")
    bench.add_argument("--n", type=int, default=400)
    bench.set_defaults(func=cmd_bench)

    args = parser.parse_args()
    if args.func is cmd_bench:
        args.endpoint = [tuple(e.split("=", 1)) for e in args.endpoint]
        if args.baseline is None:
            args.baseline = args.endpoint[0][0]
    args.func(args)


if __name__ == "__main__":
    main()
