package guard_test

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// updateSnapshots is the environment variable that rewrites the surface
// snapshots in testdata/api instead of comparing with them.
const updateSnapshots = "SCHEMAFLUX_GUARD_UPDATE_API"

// expected is the set of exported names a package is meant to have, from table
// A4 of the amendments as the later rulings changed it. Methods and fields are
// not counted, as in A4.
type expected struct {
	// pkg is the package under v2: "" for the root.
	pkg string
	// count is the number A4 gives after the amendments, and ruling says which
	// rulings and changes make it that number.
	count  int
	ruling string
	// names are the names, separated by white space.
	names string
}

var surface = []expected{
	{pkg: "", count: 220, ruling: "A4 root 206; R25 decision layer +9 (the ruling says eight and lists nine names: Decision DecisionQuestion DecisionKind DecisionYesNo DecisionChoice DecisionScale DecisionAnswer DecisionEscalation DecisionLimits); R28 +3 (Tool ToolCall Message); Chunk and Version added to the root later",
		names: `Client Default SetDefault WithClient Budget Scheduler SchedulerStats DataPolicy
Provider Request Response Usage
Recorder WithRecorder Record Asked Cost Timing Check Provenance ContractLevel ContractPromptOnly ContractJSONWellFormed ContractSchemaConstrained ContractSchemaAndInvariantChecked ContractEvidenceChecked ContractFullyGoverned
Error Kind ErrConfiguration ErrInvalidRequest ErrUnsupported ErrUnauthorized ErrPolicy ErrBudgetExceeded ErrBusy ErrRateLimited ErrUnavailable ErrTimeout ErrCanceled ErrClosed ErrTooLarge ErrTruncated ErrRefused ErrInvalidAnswer ErrReviewRequired ErrInternal
Options Tier TierSmart TierFast TierQuick Mode ModeStrict ModeTransform ModeCreative Effort EffortNone EffortMinimal EffortLow EffortMedium EffortHigh EffortXHigh EffortMax
Step Route To Branch Select Whether RouteBy Gather Gather2 Gather3 Gather4 Each
Extract ExtractOptions Transform TransformOptions Generate GenerateOptions Infer InferOptions Parse ParseOptions ParseResult
Complete CompleteField CompleteOptions CompleteResult CompleteFieldResult Suggest SuggestOptions
Annotate AnnotateOptions AnnotateResult Annotation Explain ExplainOptions ExplainResult Question QuestionOptions QuestionResult
Summarize SummarizeDetail SummarizeOptions Summary Rewrite RewriteDetail RewriteOptions Rewritten Translate TranslateDetail TranslateOptions Translation TranslationAlternative Expand ExpandDetail ExpandOptions Expansion Compress CompressOptions CompressResult Normalize NormalizeOptions NormalizeResult NormalizeChange Format Merge
StreamSummarize StreamRewrite StreamTranslate StreamExpand TextStream
Classify ClassifyOptions ClassifyResult ClassifyAlternative Score ScoreOptions ScoreResult Compare CompareOptions CompareResult ComparisonPoint Similar SimilarOptions SimilarResult AspectMatch Diff DiffOptions DiffResult DiffChange Cluster ClusterOptions ClusterResult ClusterInfo Deduplicate DeduplicateResult
Choose ChooseOptions Filter FilterOptions Sort SortOptions Rank RankOptions RankResult RankedItem SemanticMatch SemanticMatchOptions SemanticMatchResult MatchPair
Judgment JudgmentIssue Verdict VerdictUnknown VerdictPass VerdictFail VerdictMixed Validate ValidateOptions ValidateResult Verify VerifyOptions VerifyResult ClaimVerification Audit AuditOptions AuditResult Critique CritiqueOptions CritiqueResult CritiquePositive
Is IsDetail Which WhichDetail WhichOrNone WhichOrNoneDetail Rate RateDetail Assess AssessDetail Judged
Decision DecisionQuestion DecisionKind DecisionYesNo DecisionChoice DecisionScale DecisionAnswer DecisionEscalation DecisionLimits
Tool ToolCall Message
Chunk Version`},
	{pkg: "exp", count: 66, ruling: "A4 exp 66 (16 functions, 15 options types, 35 answer types)",
		names: `Synthesize Predict Assemble Derive Interpolate Arbitrate Resolve Settle SettleAdversarial Conform Project Pivot Enrich EnrichInPlace Decompose DecomposeToSlice
SynthesizeOptions PredictOptions AssembleOptions DeriveOptions InterpolateOptions ArbitrateOptions ResolveOptions SettleOptions SettleAdversarialOptions ConformOptions ProjectOptions PivotOptions EnrichOptions DecomposeOptions DecomposeToSliceOptions
SynthesizeResult SynthesisFact SynthesisInsight SynthesisConflict PredictResult PredictionInterval PredictionScenario PredictionFactor AssembleResult ComposedField DeriveResult Derivation InterpolateResult FilledItem ArbitrateResult OptionEvaluation RuleEvaluation ResolveResult Conflict SettleResult Tradeoff AdversarialContext AdversarialPosition SettleAdversarialResult TermMovement ConformResult Adjustment ProjectResult FieldMapping PivotResult PivotMapping PivotStats EnrichResult DecomposeResult DecomposedPart`},
	{pkg: "redact", count: 16, ruling: "A4 redact 16 (F21, RL1)",
		names: `Value Text ValueOptions TextOptions TextResult Span Strategy Nil Mask Remove Jumble Scramble JumbleMode JumbleBasic JumbleSmart JumbleTypeAware`},
	{pkg: "flow", count: 35, ruling: "A4 flow 35 (CQ10, CQ11, F21, O14, O19e)",
		names: `BranchAhead BranchBoth RouteAhead Escalate Until Vote ExactAgreement Checkpoint Approve MapReduce Guard Decide Match When Otherwise
EscalationRecord VoteOptions VoteRecord Reconciler ReconcileOutcome CheckpointStore CheckpointRecord CheckpointOutcome MemoryStore ApprovalGate ApprovalOutcome Review ReviewError MapReduceOptions MapReduceResult ChunkError GuardResult Decision DecideResult Case`},
	{pkg: "op", count: 62, ruling: "A4 op 57 (R5, R7, R8); R28/R29 +5 (RunEach Item Stage RunStages PreflightStages)",
		names: `New Must Run RunBatch RunMany Preflight WithParents Catalog Lookup
Descriptor ID Semantics Contract Batch Policy Options PlanOptions Plan ChunkPlan OversizedItem CostEstimate PolicySnapshot CapabilitySnapshot Coverage Shape Algebra BatchClass Category Stability Entry
ShapeUnspecified ShapeAtomic ShapeBatched ShapeSplit ShapeGlobal AlgebraIndependent AlgebraSubset AlgebraPermutation AlgebraPartition AlgebraGraph AlgebraHierarchical AlgebraSequential BatchUnspecified BatchNone BatchItemwise BatchAggregate CategoryExtract CategoryTransform CategoryGenerate CategoryClassify CategorySelect CategoryOrder CategoryReview CategoryValidate StabilityUnspecified StabilityExperimental StabilityStable
RunEach Item Stage RunStages PreflightStages`},
	{pkg: "chat", count: 1, ruling: "R28 and R29 Q6: package chat with one type, Session",
		names: `Session`},
	{pkg: "provider", count: 15, ruling: "A4 provider 14 after Chunk removal and Decider addition; keyless Codex registration adds RegisterKeyless",
		names: `New OpenAICompatible Register RegisterKeyless Names DeclareRoute Factory Config Models EndpointPolicy Streamer Warmer Retrier Unwrapper Decider`},
	{pkg: "provider/codex", count: 3, ruling: "A4 codex 3",
		names: `Options New Register`},
	{pkg: "mw", count: 29, ruling: "A4 mw 26 (R16); R25 +2 (DecisionOptions, Decisions); custom middleware construction adds Wrap",
		names: `Middleware Chain Func Wrap Retry RetryOptions RateLimit RateLimitOptions Bulkhead BulkheadOptions Cache CacheOptions WithCachePartition CircuitBreaker BreakerOptions Fallback FallbackOptions Hedge Budget BudgetOptions Redact RedactOptions CacheStore CacheEvent CachePartition Route Capabilities DecisionOptions Decisions`},
	{pkg: "pricing", count: 17, ruling: "A4 pricing 16; SetBudget is SetLimits and Lookup is ForRequest (renames); Breakdown returns the new type Spending (+1)",
		names: `Register Models Model Price Spent Entries ForRequest Entry Summarize Summary Breakdown Export SetLimits Limits SetHistoryLimit Reset Spending`},
	{pkg: "telemetry", count: 19, ruling: "A4 telemetry 15; Note NoteObserver NoteRetry NoteHedge added (+4)",
		names: `Observer Event Result Scope ScopeRequest ScopeOperation RegisterObserver Sink Metric Snapshot RegisterSink Snapshots SetSpanIDSources TraceID SpanID Note NoteObserver NoteRetry NoteHedge`},
	{pkg: "telemetry/otel", count: 4, ruling: "A4 otel 4 (R21)",
		names: `Observer New Install InstallIDSources`},
	{pkg: "requestid", count: 18, ruling: "A4 requestid 19 (D3: the constants' names are the lane's, prefixed by their type); CorrelationRequest removed (-1)",
		names: `With WithCorrelation From IDs Inject Extract Config Configure Strategy Correlation StrategyAuto StrategyUUID StrategyTimestamp StrategyTrace StrategyNone CorrelationInherit CorrelationGenerate CorrelationNone`},
	{pkg: "schemafluxtest", count: 20, ruling: "A4 schemafluxtest 12; R25 adds six scripted decider names; tool-call cassettes and a context-aware test wait add two",
		names: `Provider Install Record Tape Replay ReplayFrom Player Cassette CassetteRequest CassetteResponse CassetteToolCall CassetteFailure Logs Wait Decider NewDecider Yes No Chose Level`},
}

// kind names what an exported object is.
func kind(o types.Object) string {
	switch o.(type) {
	case *types.Func:
		return "func"
	case *types.Const:
		return "const"
	case *types.Var:
		return "var"
	case *types.TypeName:
		return "type"
	}
	return "name"
}

// exportedNames returns the sorted exported package-level names of pkg.
func exportedNames(pkg *types.Package) []string {
	var out []string
	for _, name := range pkg.Scope().Names() {
		if token.IsExported(name) {
			out = append(out, name)
		}
	}
	return out
}

// snapshotLines prints the exported surface of pkg as go/types sees it, one
// declaration per line, sorted by name; a type is followed by its exported
// methods. Unexported fields and methods are left out: the snapshot is what a
// caller can name.
func snapshotLines(pkg *types.Package) []string {
	q := func(p *types.Package) string {
		if p == pkg {
			return ""
		}
		return p.Name()
	}
	var out []string
	for _, name := range exportedNames(pkg) {
		obj := pkg.Scope().Lookup(name)
		switch o := obj.(type) {
		case *types.TypeName:
			out = append(out, typeLine(o, q))
			if named, ok := o.Type().(*types.Named); ok {
				var methods []string
				for i := 0; i < named.NumMethods(); i++ {
					m := named.Method(i)
					if m.Exported() {
						methods = append(methods, "method "+strings.TrimPrefix(types.ObjectString(m, q), "func "))
					}
				}
				sort.Strings(methods)
				out = append(out, methods...)
			}
		default:
			out = append(out, types.ObjectString(obj, q))
		}
	}
	return out
}

// typeLine prints a type declaration with its exported fields (a struct), its
// exported methods (an interface) or its underlying type.
func typeLine(o *types.TypeName, q types.Qualifier) string {
	head := "type " + o.Name()
	if o.IsAlias() {
		return head + " = " + types.TypeString(o.Type(), q)
	}
	named, ok := o.Type().(*types.Named)
	if !ok {
		return head + " " + types.TypeString(o.Type(), q)
	}
	if tps := named.TypeParams(); tps.Len() > 0 {
		var parts []string
		for i := 0; i < tps.Len(); i++ {
			tp := tps.At(i)
			parts = append(parts, tp.Obj().Name()+" "+types.TypeString(tp.Constraint(), q))
		}
		head += "[" + strings.Join(parts, ", ") + "]"
	}
	switch u := named.Underlying().(type) {
	case *types.Struct:
		var fields []string
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			if !f.Exported() {
				continue
			}
			if f.Embedded() {
				fields = append(fields, "embedded "+types.TypeString(f.Type(), q))
				continue
			}
			fields = append(fields, f.Name()+" "+types.TypeString(f.Type(), q))
		}
		return head + " struct{" + strings.Join(fields, "; ") + "}"
	case *types.Interface:
		var methods []string
		for i := 0; i < u.NumMethods(); i++ {
			m := u.Method(i)
			if m.Exported() {
				methods = append(methods, m.Name()+strings.TrimPrefix(types.TypeString(m.Type(), q), "func"))
			}
		}
		return head + " interface{" + strings.Join(methods, "; ") + "}"
	default:
		return head + " " + types.TypeString(u, q)
	}
}

// snapshotFile is the file a package's snapshot is kept in.
func snapshotFile(name string) string {
	file := "schemaflux"
	if name != "" {
		file = strings.ReplaceAll(name, "/", "_")
	}
	return filepath.Join("testdata", "api", file+".txt")
}

func TestSurfaceMatchesTheSnapshots(t *testing.T) {
	s := loadSource(t)
	update := os.Getenv(updateSnapshots) != ""
	for _, path := range s.paths {
		name := short(path)
		t.Run(label(path), func(t *testing.T) {
			pkg := requirePackage(t, s, name)
			got := strings.Join(snapshotLines(pkg), "\n") + "\n"
			file := snapshotFile(name)
			if update {
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("no snapshot of %s (%v): generate it with %s=1", label(path), err, updateSnapshots)
			}
			if string(want) != got {
				t.Errorf("the surface of %s differs from %s; first difference:\n%s\nregenerate with %s=1 if the change is meant",
					label(path), file, firstDifference(string(want), got), updateSnapshots)
			}
		})
	}
}

// firstDifference names the first line two texts do not share.
func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return fmt.Sprintf("  snapshot: %s\n  tree:     %s", a, b)
		}
	}
	return "  (none)"
}

func TestSurfaceCountsMatchTheAmendedA4(t *testing.T) {
	s := loadSource(t)
	byPkg := map[string]expected{}
	for _, e := range surface {
		byPkg[e.pkg] = e
		if n := len(strings.Fields(e.names)); n != e.count {
			t.Errorf("the table itself is inconsistent for %s: %d names listed, count %d (%s)", labelOf(e.pkg), n, e.count, e.ruling)
		}
	}
	for _, e := range surface {
		t.Run(labelOf(e.pkg), func(t *testing.T) {
			pkg := requirePackage(t, s, e.pkg)
			have := map[string]bool{}
			for _, n := range exportedNames(pkg) {
				have[n] = true
			}
			var missing, extra []string
			for _, n := range strings.Fields(e.names) {
				if !have[n] {
					missing = append(missing, n)
				}
				delete(have, n)
			}
			for n := range have {
				extra = append(extra, n)
			}
			sort.Strings(missing)
			sort.Strings(extra)
			total := len(exportedNames(pkg))
			if total != e.count || len(missing) > 0 || len(extra) > 0 {
				t.Errorf("%s has %d exported names, A4 as amended says %d (%s)\n  expected and not in the tree: %v\n  in the tree and not expected: %v",
					labelOf(e.pkg), total, e.count, e.ruling, missing, extra)
			}
		})
	}
	for _, path := range s.paths {
		if _, ok := byPkg[short(path)]; !ok {
			t.Errorf("package %s exists and has no row in the table of expected names", label(path))
		}
	}
}

func labelOf(pkg string) string {
	if pkg == "" {
		return "v2"
	}
	return "v2/" + pkg
}

// startsWithName reports whether a doc comment begins with the name it
// documents, allowing the article form godoc accepts for a type ("A Client").
func startsWithName(doc, name string) bool {
	text := strings.TrimSpace(doc)
	for _, article := range []string{"A ", "An ", "The "} {
		text = strings.TrimPrefix(text, article)
	}
	return strings.HasPrefix(text, name)
}

func commentText(g *ast.CommentGroup) string {
	if g == nil {
		return ""
	}
	return g.Text()
}

func TestEveryExportedNameIsDocumented(t *testing.T) {
	s := loadSource(t)
	for _, path := range s.paths {
		name := short(path)
		t.Run(label(path), func(t *testing.T) {
			requirePackage(t, s, name)
			var problems []string
			add := func(pos token.Pos, format string, args ...any) {
				p := s.fset.Position(pos)
				problems = append(problems, fmt.Sprintf("%s:%d: %s", filepath.Base(p.Filename), p.Line, fmt.Sprintf(format, args...)))
			}
			for _, f := range s.files[path] {
				for _, decl := range f.Decls {
					switch d := decl.(type) {
					case *ast.FuncDecl:
						if !d.Name.IsExported() {
							continue
						}
						if d.Recv != nil && !receiverExported(d.Recv) {
							continue
						}
						doc := commentText(d.Doc)
						if doc == "" {
							add(d.Pos(), "%s has no doc comment", funcName(d))
						} else if !startsWithName(doc, d.Name.Name) {
							add(d.Pos(), "the doc comment of %s does not start with its name", funcName(d))
						}
					case *ast.GenDecl:
						for _, spec := range d.Specs {
							switch sp := spec.(type) {
							case *ast.TypeSpec:
								if !sp.Name.IsExported() {
									continue
								}
								doc := commentText(sp.Doc)
								if doc == "" && len(d.Specs) == 1 {
									doc = commentText(d.Doc)
								}
								if doc == "" {
									add(sp.Pos(), "type %s has no doc comment", sp.Name.Name)
								} else if !startsWithName(doc, sp.Name.Name) {
									add(sp.Pos(), "the doc comment of type %s does not start with its name", sp.Name.Name)
								}
								if st, ok := sp.Type.(*ast.StructType); ok && isOptionsType(sp.Name.Name) {
									for i, field := range st.Fields.List {
										for _, fname := range field.Names {
											if fname.IsExported() && !fieldDocumented(s.fset, st, i, fname.Name) {
												add(fname.Pos(), "field %s.%s has no comment", sp.Name.Name, fname.Name)
											}
										}
									}
								}
							case *ast.ValueSpec:
								for _, n := range sp.Names {
									if !n.IsExported() {
										continue
									}
									doc := commentText(sp.Doc)
									group := commentText(d.Doc)
									switch {
									case doc != "" && startsWithName(doc, n.Name):
									case doc == "" && commentText(sp.Comment) != "":
									case doc == "" && group != "" && strings.Contains(group, n.Name):
									case doc == "" && group != "" && len(d.Specs) > 1:
										// a group comment that introduces the whole group
									case doc == "":
										add(n.Pos(), "%s has no doc comment", n.Name)
									default:
										add(n.Pos(), "the doc comment of %s does not start with its name", n.Name)
									}
								}
							}
						}
					}
				}
			}
			if len(problems) > 0 {
				t.Errorf("%d exported names are undocumented or documented without their name:\n  %s", len(problems), strings.Join(problems, "\n  "))
			}
		})
	}
}

// fieldDocumented reports whether field i of a struct has a comment: its own, or
// the comment of the field just above it when that comment names it, which is how
// a run of fields that share one sentence is written.
func fieldDocumented(fset *token.FileSet, st *ast.StructType, i int, name string) bool {
	field := st.Fields.List[i]
	if commentText(field.Doc) != "" || commentText(field.Comment) != "" {
		return true
	}
	if i == 0 {
		return false
	}
	prev := st.Fields.List[i-1]
	text := commentText(prev.Doc) + commentText(prev.Comment)
	adjacent := fset.Position(prev.End()).Line+1 >= fset.Position(field.Pos()).Line
	return adjacent && strings.Contains(text, name)
}

// TestFieldsOfResultTypesAreDocumented reports an exported field of an exported
// struct that is not an options type and has no comment. Table A4 counts names
// and not fields, so this is held to the standard of AGENTS.md, that every
// exported name is documented, and kept apart from the names themselves.
func TestFieldsOfResultTypesAreDocumented(t *testing.T) {
	s := loadSource(t)
	for _, path := range s.paths {
		name := short(path)
		t.Run(label(path), func(t *testing.T) {
			requirePackage(t, s, name)
			var missing []string
			for _, f := range s.files[path] {
				for _, decl := range f.Decls {
					d, ok := decl.(*ast.GenDecl)
					if !ok {
						continue
					}
					for _, spec := range d.Specs {
						sp, ok := spec.(*ast.TypeSpec)
						if !ok || !sp.Name.IsExported() || isOptionsType(sp.Name.Name) {
							continue
						}
						st, ok := sp.Type.(*ast.StructType)
						if !ok {
							continue
						}
						for i, field := range st.Fields.List {
							for _, fname := range field.Names {
								if fname.IsExported() && !fieldDocumented(s.fset, st, i, fname.Name) {
									missing = append(missing, sp.Name.Name+"."+fname.Name)
								}
							}
						}
					}
				}
			}
			if len(missing) > 0 {
				t.Errorf("%d exported fields have no comment: %s", len(missing), strings.Join(missing, " "))
			}
		})
	}
}

func receiverExported(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) == 0 {
		return true
	}
	expr := recv.List[0].Type
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.IsExported()
		default:
			return false
		}
	}
}

func funcName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	expr := d.Recv.List[0].Type
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name + "." + d.Name.Name
		default:
			return d.Name.Name
		}
	}
}

var (
	zeroMeaning  = regexp.MustCompile(`(?i)\b(zero|default|empty|nil|unset|none|no limit|unlimited|false|disabled|off|omit|omitted|without|neither|not set|leave|leaves|left|unchanged|no [a-z]+)\b`)
	boundMeaning = regexp.MustCompile(`(?i)(negative|outside|below|above|between|at least|at most|range|greater|less than|exceed|beyond|up to|more than|fewer|maximum|minimum|no more|no fewer|ceiling|floor|must be|from \d)`)
)

// isOptionsType reports whether an exported struct is one whose fields the
// caller sets to configure a call or a client.
func isOptionsType(name string) bool {
	switch name {
	case "Client", "DataPolicy", "Budget", "Scheduler", "Recorder", "Config", "Limits":
		return true
	}
	return strings.HasSuffix(name, "Options")
}

func TestOptionsFieldsSayWhatTheirZeroMeans(t *testing.T) {
	s := loadSource(t)
	for _, path := range s.paths {
		name := short(path)
		t.Run(label(path), func(t *testing.T) {
			requirePackage(t, s, name)
			var noZero, noBound []string
			for _, f := range s.files[path] {
				for _, decl := range f.Decls {
					d, ok := decl.(*ast.GenDecl)
					if !ok {
						continue
					}
					for _, spec := range d.Specs {
						sp, ok := spec.(*ast.TypeSpec)
						if !ok || !sp.Name.IsExported() || !isOptionsType(sp.Name.Name) {
							continue
						}
						st, ok := sp.Type.(*ast.StructType)
						if !ok {
							continue
						}
						for _, field := range st.Fields.List {
							if len(field.Names) == 0 {
								continue // embedded: documented where it is declared
							}
							text := commentText(field.Doc) + " " + commentText(field.Comment)
							for _, fname := range field.Names {
								if !fname.IsExported() {
									continue
								}
								where := fmt.Sprintf("%s.%s", sp.Name.Name, fname.Name)
								if strings.TrimSpace(text) == "" {
									continue // reported by the documentation test
								}
								if !zeroMeaning.MatchString(text) {
									noZero = append(noZero, where)
								}
								if isNumber(field.Type) && !boundMeaning.MatchString(text) {
									noBound = append(noBound, where)
								}
							}
						}
					}
				}
			}
			if len(noZero) > 0 {
				t.Errorf("%d fields of options types do not say what their zero value means: %s", len(noZero), strings.Join(noZero, " "))
			}
			if len(noBound) > 0 {
				t.Errorf("%d numeric fields of options types do not say what a negative or out-of-range value does: %s", len(noBound), strings.Join(noBound, " "))
			}
		})
	}
}

// isNumber reports whether a field's type, as written, is a number or a
// duration. A rune and an unsigned size are numbers a negative cannot reach, and
// a seed has no range, so neither is held to the rule.
func isNumber(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		switch e.Name {
		case "int", "int8", "int16", "int32", "int64", "float32", "float64":
			return e.Name != "int32" // int32 is a rune in this library's fields
		}
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok && x.Name == "time" && e.Sel.Name == "Duration" {
			return true
		}
	}
	return false
}
