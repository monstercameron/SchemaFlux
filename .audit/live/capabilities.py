"""P-013: capability matrix across the gpt-5.6 family, measured not assumed.

supportsTemperature() and supportsReasoningControls() pattern-match on model
name prefixes. A prefix rule is a guess about a family that did not exist when
the rule was written, and sending an unaccepted parameter fails the WHOLE
request -- so a wrong guess here is not a degraded call, it is no call at all.

This probes each capability against each model, one parameter at a time, and
prints what the API actually accepts. It is the evidence P-010 and P-011 need.

    python .audit/live/capabilities.py

Costs a few cents. Reads OPENAI from .env; never prints the key.
"""

import json
import os
import re
import sys
import time
import urllib.error
import urllib.request

MODELS = ["gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"]
ENDPOINT = "https://api.openai.com/v1/responses"


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

# The strict schema the library actually generates, so this probes the real
# artifact rather than a simplified stand-in.
SCHEMA = {
    "type": "object",
    "additionalProperties": False,
    "required": ["vendor", "total"],
    "properties": {
        "vendor": {"type": "string"},
        "total": {"type": "number"},
    },
}


def call(model, extra, prompt="Invoice from Acme, total $12.50."):
    """One request. Returns (ok, detail, elapsed, usage)."""
    body = {
        "model": model,
        "input": prompt,
        "instructions": "Extract the invoice.",
    }
    body.update(extra)

    request = urllib.request.Request(
        ENDPOINT,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "Authorization": f"Bearer {KEY}"},
    )

    start = time.time()
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            payload = json.load(response)
    except urllib.error.HTTPError as err:
        detail = err.read().decode()[:200].replace("\n", " ")
        return False, f"{err.code}: {detail}", time.time() - start, {}
    except Exception as err:  # noqa: BLE001 - report whatever went wrong
        return False, str(err)[:200], time.time() - start, {}

    text = ""
    for item in payload.get("output", []):
        if item.get("type") not in (None, "message"):
            continue
        for chunk in item.get("content", []):
            if chunk.get("type") in (None, "output_text"):
                text += chunk.get("text", "")

    return True, text[:120].replace("\n", " "), time.time() - start, payload.get("usage", {})


PROBES = [
    ("baseline", {}),
    ("temperature", {"temperature": 0.7}),
    ("temperature_zero", {"temperature": 0.0}),
    ("json_object", {"text": {"format": {"type": "json_object"}}}),
    ("json_schema_strict", {"text": {"format": {
        "type": "json_schema", "name": "invoice", "strict": True, "schema": SCHEMA}}}),
    ("reasoning_minimal", {"reasoning": {"effort": "minimal"}}),
    ("reasoning_none", {"reasoning": {"effort": "none"}}),
    ("reasoning_low", {"reasoning": {"effort": "low"}}),
    ("reasoning_medium", {"reasoning": {"effort": "medium"}}),
    ("reasoning_high", {"reasoning": {"effort": "high"}}),
    ("verbosity_low", {"text": {"verbosity": "low"}}),
    ("store_false", {"store": False}),
    ("max_output_tokens", {"max_output_tokens": 256}),
    ("prompt_cache_key", {"prompt_cache_key": "schemaflux-probe-v1"}),
]

results = {}
print(f"{'model':<16} {'probe':<20} {'ok':<4} {'ms':>6}  detail")
print("-" * 100)

for model in MODELS:
    results[model] = {}
    for name, extra in PROBES:
        ok, detail, elapsed, usage = call(model, extra)
        results[model][name] = ok
        flag = "yes" if ok else "NO"
        print(f"{model:<16} {name:<20} {flag:<4} {elapsed*1000:>6.0f}  {detail}")
        time.sleep(0.3)

print()
print("SUMMARY -- what each model accepts")
print(f"{'probe':<22} " + " ".join(f"{m.replace('gpt-5.6-',''):<8}" for m in MODELS))
print("-" * 60)
for name, _ in PROBES:
    row = " ".join(f"{'yes' if results[m][name] else 'NO':<8}" for m in MODELS)
    print(f"{name:<22} {row}")

# Cached-token reporting needs two identical calls: the second should report
# cached input tokens if the prefix was retained. The prompt has to be long
# enough to be worth caching, which is why this is separate from the probes.
print()
print("CACHED TOKEN REPORTING (two identical long calls)")
long_prompt = ("Invoice from Acme Corporation. " * 400) + "The total is $12.50."
for model in MODELS:
    seen = []
    for attempt in (1, 2):
        ok, _, _, usage = call(model, {}, prompt=long_prompt)
        details = usage.get("input_tokens_details", {})
        seen.append((usage.get("input_tokens"), details.get("cached_tokens"),
                     usage.get("output_tokens_details", {}).get("reasoning_tokens")))
        time.sleep(0.5)
    print(f"{model:<16} call1 in/cached/reasoning={seen[0]}  call2={seen[1]}")
