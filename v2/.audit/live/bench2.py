import json, os, subprocess, time, statistics

KEY = os.environ["OPENAI"]
MODELS = ["gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"]
RUNS = 4

# Reasoning that a fast model plausibly gets wrong: units, an implicit
# proration, and a distractor figure.
PROMPT = (
    "Return ONLY JSON: {\"monthly_cost\": <number>, \"reasoning\": <string>}. "
    "A team pays $4,800 for an annual plan covering 8 seats. Mid-year they add 4 more seats, "
    "billed pro rata for the remaining 6 months at the same per-seat annual rate. "
    "What is the AVERAGE monthly cost across the full year? "
    "Ignore the $250 one-time setup fee.")

# 4800 + (4 * 600 * 0.5) = 4800 + 1200 = 6000 / 12 = 500
EXPECTED = 500.0

def call(model):
    body = json.dumps({"model": model, "input": PROMPT})
    start = time.time()
    out = subprocess.run(
        ["curl", "-s", "--max-time", "120", "https://api.openai.com/v1/responses",
         "-H", f"Authorization: Bearer {KEY}", "-H", "Content-Type: application/json",
         "-d", body], capture_output=True, text=True).stdout
    elapsed = (time.time() - start) * 1000
    d = json.loads(out)
    text = ""
    for o in d.get("output", []):
        for c in (o.get("content") or []):
            if c.get("type") == "output_text":
                text = c.get("text", "")
    return elapsed, text, d.get("usage", {})

for model in MODELS:
    lats, correct, answers, reasoning_toks = [], 0, [], []
    for _ in range(RUNS):
        ms, text, usage = call(model)
        lats.append(ms)
        reasoning_toks.append(usage.get("output_tokens_details", {}).get("reasoning_tokens", 0))
        cleaned = text.strip().removeprefix("```json").removeprefix("```").removesuffix("```").strip()
        try:
            value = json.loads(cleaned).get("monthly_cost")
            answers.append(value)
            if isinstance(value, (int, float)) and abs(value - EXPECTED) < 0.01:
                correct += 1
        except Exception:
            answers.append(None)
    print(f"{model:16s} median={statistics.median(lats):7.0f}ms  correct={correct}/{RUNS}  "
          f"reasoning_tokens={statistics.median(reasoning_toks):.0f}  answers={answers}")
