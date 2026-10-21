import json, os, subprocess, time, statistics, sys

KEY = os.environ["OPENAI"]
MODELS = ["gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"]
RUNS = 4

PROMPT = ("Return ONLY a JSON object with keys number (string), total (number, no currency "
          "symbol or separators), vendor (string), paid (boolean). "
          "Input: Invoice INV-4417 from Northwind Traders, total $1,284.50, still outstanding.")

def call(model):
    body = json.dumps({"model": model, "input": PROMPT})
    start = time.time()
    out = subprocess.run(
        ["curl", "-s", "--max-time", "90", "https://api.openai.com/v1/responses",
         "-H", f"Authorization: Bearer {KEY}", "-H", "Content-Type: application/json",
         "-d", body],
        capture_output=True, text=True).stdout
    elapsed = (time.time() - start) * 1000
    d = json.loads(out)
    text = ""
    for o in d.get("output", []):
        for c in (o.get("content") or []):
            if c.get("type") == "output_text":
                text = c.get("text", "")
    return elapsed, text, d.get("usage", {})

for model in MODELS:
    lats, typed_ok = [], 0
    for _ in range(RUNS):
        ms, text, usage = call(model)
        lats.append(ms)
        cleaned = text.strip().removeprefix("```json").removeprefix("```").removesuffix("```").strip()
        try:
            parsed = json.loads(cleaned)
            if isinstance(parsed.get("total"), (int, float)) and isinstance(parsed.get("paid"), bool):
                typed_ok += 1
        except Exception:
            pass
    print(f"{model:16s} median={statistics.median(lats):7.0f}ms  "
          f"p_min={min(lats):6.0f}  p_max={max(lats):6.0f}  types_correct={typed_ok}/{RUNS}")
