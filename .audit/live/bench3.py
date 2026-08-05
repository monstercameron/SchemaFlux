"""P-011: does enabling reasoning controls on the 5.6 family cost accuracy?

The capability probe found that `reasoning.effort: "minimal"` -- the value
reasoningEffort() returns for anything that is not gpt-5.4 -- is REJECTED by
the whole 5.6 family. Accepted values are none/low/medium/high/xhigh/max. So
the library is one flag away from 400ing every request, and supportsReasoning
Controls() returning false for gpt-5.6 is the only thing hiding it.

Fixing the value is unambiguous. Whether to then SEND it is not: omitting the
block leaves the server's own default in charge, and "none" is not obviously
the same thing. This measures the difference on a task a fast model plausibly
gets wrong -- an implicit proration with a distractor figure -- so the choice
rests on evidence rather than on the word "minimal" sounding cheap.

    python .audit/live/bench3.py

Reads OPENAI from .env; never prints the key. Costs a few cents.
"""

import json
import os
import re
import statistics
import sys
import time
import urllib.error
import urllib.request

MODELS = ["gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"]
RUNS = 4
ENDPOINT = "https://api.openai.com/v1/responses"

PROMPT = (
    'Return ONLY JSON: {"monthly_cost": <number>, "reasoning": <string>}. '
    "A team pays $4,800 for an annual plan covering 8 seats. Mid-year they add 4 more seats, "
    "billed pro rata for the remaining 6 months at the same per-seat annual rate. "
    "What is the AVERAGE monthly cost across the full year? "
    "Ignore the $250 one-time setup fee.")

# 4800 + (4 * 600 * 0.5) = 6000 / 12 = 500
EXPECTED = 500.0

# What the library would send, versus what it sends today.
ARMS = [
    ("omitted (today)", {}),
    ("effort=none", {"reasoning": {"effort": "none"}}),
    ("effort=none+verbosity", {"reasoning": {"effort": "none"}, "text": {"verbosity": "low"}}),
    ("effort=low", {"reasoning": {"effort": "low"}}),
]


def load_key():
    for name in ("SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY", "OPENAI"):
        if os.environ.get(name):
            return os.environ[name]
    try:
        with open(".env", encoding="utf-8") as handle:
            for line in handle:
                match = re.match(r"^(?:OPENAI|OPENAI_API_KEY)\s*=\s*(.+)$", line.strip())
                if match:
                    return match.group(1).strip().strip("\"'")
    except FileNotFoundError:
        pass
    sys.exit("no OpenAI key found in the environment or .env")


KEY = load_key()


def call(model, extra):
    body = {"model": model, "input": PROMPT}
    body.update(extra)
    request = urllib.request.Request(
        ENDPOINT,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "Authorization": f"Bearer {KEY}"},
    )
    start = time.time()
    try:
        with urllib.request.urlopen(request, timeout=180) as response:
            payload = json.load(response)
    except urllib.error.HTTPError as err:
        return (time.time() - start) * 1000, None, 0, err.read().decode()[:120]

    text = ""
    for item in payload.get("output", []):
        if item.get("type") not in (None, "message"):
            continue
        for chunk in item.get("content", []):
            if chunk.get("type") in (None, "output_text"):
                text += chunk.get("text", "")

    cleaned = text.strip().removeprefix("```json").removeprefix("```").removesuffix("```").strip()
    value = None
    try:
        value = json.loads(cleaned).get("monthly_cost")
    except Exception:  # noqa: BLE001 - an unparseable answer is a wrong answer
        pass

    reasoning = payload.get("usage", {}).get("output_tokens_details", {}).get("reasoning_tokens", 0)
    return (time.time() - start) * 1000, value, reasoning, ""


print(f"{'model':<16} {'arm':<24} {'correct':>8} {'p50 ms':>8} {'reasoning tok':>14}  answers")
print("-" * 100)

for model in MODELS:
    for label, extra in ARMS:
        latencies, answers, reasoning_tokens, correct, error = [], [], [], 0, ""
        for _ in range(RUNS):
            elapsed, value, reasoning, err = call(model, extra)
            if err:
                error = err
                break
            latencies.append(elapsed)
            answers.append(value)
            reasoning_tokens.append(reasoning)
            if isinstance(value, (int, float)) and abs(value - EXPECTED) < 0.01:
                correct += 1
            time.sleep(0.3)

        if error:
            print(f"{model:<16} {label:<24} {'REJECTED':>8}  {error}")
            continue

        print(f"{model:<16} {label:<24} {f'{correct}/{RUNS}':>8} "
              f"{statistics.median(latencies):>8.0f} {statistics.mean(reasoning_tokens):>14.0f}"
              f"  {answers}")
