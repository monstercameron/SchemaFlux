import io, re, collections, glob, os, sys

passes = [l.strip() for l in io.open('.audit/passes.txt', encoding='utf-8') if l.strip()]
names = set(passes)
children = collections.Counter()
for n in names:
    if '/' in n:
        children[n.rsplit('/', 1)[0]] += 1
leaves = [n for n in names if children[n] == 0]

by_top = collections.Counter()
for n in leaves:
    by_top[n.split('/')[0]] += 1

def funcs_in(path):
    if not os.path.exists(path):
        return []
    src = io.open(path, encoding='utf-8').read()
    return re.findall(r'^func (Test\w+)\(', src, re.M)

def examples_in(paths):
    out = []
    for p in paths:
        if not os.path.exists(p):
            continue
        src = io.open(p, encoding='utf-8').read()
        out += re.findall(r'^func (Example\w*)\(', src, re.M)
    return out

# cluster -> (files whose Test funcs all belong here, extra explicit func names, smart+? )
CLUSTERS = [
 ("P-001..P-006  Responses API parsing", ["internal/llm/provider_responses_test.go","internal/llm/provider_shapes_test.go"], [], False),
 ("P-010/P-011   model defaults",        ["internal/config/model_defaults_test.go"], [], False),
 ("PR-001/PR-007 pricing",               ["pricing/unpriced_test.go"], [], False),
 ("F-012..F-014/B-02/B-03  Init + .env", ["client_env_test.go"], [], False),
 ("F-001/F-003/F-004  Validate",         ["internal/ops/validate_failopen_test.go","validate_integration_test.go"], [], True),
 ("F-002  text *WithMetadata",           ["internal/ops/text_failopen_test.go","text_integration_test.go"], [], True),
 ("F-033  ParseJSON redaction",          ["internal/ops/json_redaction_test.go"], [], False),
 ("PERF-001/002  retry policy",          ["internal/ops/retry_policy_test.go","internal/ops/retry_policy_cases_test.go"], [], False),
 ("X-01  embedded options merge",        ["internal/ops/embedded_options_test.go"], [], False),
 ("F-005/F-006  Decide",                 ["internal/ops/decide_failopen_test.go","controlflow_integration_test.go"], ["TestDecide"], True),
 ("F-007/F-008  Match/Otherwise",        ["internal/ops/control_flow_failopen_test.go","controlflow_integration_test.go"], [], True),
 ("F-009  Compose",                      [], ["TestCompose","TestComposeThreadsEveryOperation","TestComposeStopsAtTheFailingOperation","TestComposeDegenerateInputs"], False),
 ("F-010/F-011  Pipeline retries",       ["internal/ops/pipeline_retry_test.go"], [], False),
 ("F-015  json:\"-\" in schema",         [], ["TestSchemaOmitsFieldsExcludedFromJSON","TestSchemaJSONTagHandling","TestSchemaWithEveryFieldExcluded","TestHasJSONOption","TestIntegrationExcludedFieldsNeverReachTheProvider"], False),
 ("CA-001  prompt determinism",          [], ["TestPromptsAreByteIdenticalAcrossRuns","TestSortedKeys","TestSortedKeysOrdering","TestSortedKeysIsStableAcrossCalls","TestIntegrationRepeatedCallsSendIdenticalBytes"], False),
 ("F-016..F-019  fabricated numbers",    [], ["TestFailedExtractionCarriesNoInventedConfidence","TestCompleteReportsNoInventedConfidence","TestNoConfidenceIsInventedFromTextShape","TestFailedExtractionNeverCarriesAConfidence"], False),
 ("F-029/F-031  client + locks",         ["client_concurrency_test.go"], [], False),
 ("F-030  ErrNoProvider",                [], ["TestNoProviderErrorNamesTheWayOut","TestUninitialisedOperationErrorNamesTheWayOut","TestEveryOperationReportsErrNoProvider","TestErrNoProviderMessageIsActionable"], False),
 ("F-034  error payloads",               ["internal/ops/error_payload_test.go"], [], False),
 ("F-025  shell gate",                   ["internal/tools/shell_policy_test.go"], ["TestShellToolBasic","TestShellToolWithShell","TestShellToolExitCode","TestShellToolMissingCommand","TestShellToolInvalidCommand","TestShellToolTimeout"], False),
 ("F-026  stub honesty",                 ["internal/tools/stub_honesty_test.go"], ["TestTokenToolRefusesJWTRatherThanFakingIt","TestTokenToolGeneratesRandomTokens","TestGenerateRandomToken","TestGeneratedTokensAreUnpredictable","TestTokenTool","TestEveryStubToolIsIdentifiable","TestNonStubToolsDoNotReturnStubResults","TestTokenLengths"], False),
 ("F-027  registry errors",              [], ["TestRegisterReportsADuplicate","TestMustRegisterPanicsOnADuplicate","TestUnregister","TestRegistryDefaultRegistry","TestRegisterRejectsBadRegistrations","TestFailedRegistrationLeavesTheRegistryIntact","TestUnregisterReportsWhatItDid","TestRegisterUnregisterRoundTrip","TestMustRegisterPanicMessageNamesTheTool"], False),
 ("F-028/F-035..F-038  exit gate",       ["faultinjection_integration_test.go","internal/ops/json_strict_test.go"], [], False),
 ("F-020  score provenance",            ["internal/ops/provenance_test.go"], [], False),
 ("F-021/F-022  disclosures",           ["internal/ops/disclosure_test.go"], [], False),
 ("OP-301  Project.Exclude",            ["internal/ops/project_exclude_test.go","project_integration_test.go"], [], True),
 ("F-023/F-024  dead options",          ["internal/ops/dead_options_test.go","internal/ops/options_reach_prompt_test.go"], [], False),
 ("OP-102/OP-103  collection identity", ["internal/ops/collection_identity_test.go","collection_integration_test.go"], [], True),
 ("OP-403  rune safety",                ["internal/ops/runes_test.go"], [], False),
 ("S-005  response format",             ["internal/ops/response_format_test.go"], [], False),
 ("FL-001  provider models",            ["internal/config/provider_models_test.go"], [], False),
 ("FL-002  builder wiring",             ["internal/api/fluent/wiring_test.go"], [], False),
 ("B-01/P-012  live verification",      ["live_provider_test.go"], [], "gated"),
]

print(f"{'cluster':<40} {'cases':>6} {'ex':>3}  status")
print("-" * 66)
gaps = []
for label, files, extra, smartplus in CLUSTERS:
    gated = smartplus == "gated"
    fns = set(extra)
    for f in files:
        fns |= set(funcs_in(f))
    total = sum(by_top[f] for f in fns)
    ex = len(examples_in(files))
    flags = []
    if gated:
        # These skip unless SCHEMAFLUX_LIVE_TESTS=1, so they never appear in a
        # default run. Counting them as uncovered would be wrong, and counting
        # them as covered from a run that skipped them would be a lie. They are
        # reported separately and verified by the live run.
        pass
    else:
        if total < 10:
            flags.append("CASES")
        if smartplus and ex == 0:
            flags.append("EXAMPLE")
    status = "GATED (run with SCHEMAFLUX_LIVE_TESTS=1)" if gated else ("OK" if not flags else "NEEDS " + "+".join(flags))
    if flags:
        gaps.append((label, total, ex, flags))
    print(f"{label:<40} {total:>6} {ex:>3}  {status}")

print()
print(f"leaf cases counted: {len(leaves)}   total PASS lines: {len(names)}")
if gaps:
    print("\nGAPS:")
    for label, total, ex, flags in gaps:
        print(f"  {label}: {total} cases, {ex} examples -> {'+'.join(flags)}")
