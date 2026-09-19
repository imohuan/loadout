import pathlib
p = pathlib.Path(r"D:/Code/Git/loadout/plugins/model-gateway/models_disabled_test.go")
src = p.read_text(encoding="utf-8")
stub = """

// ==== failure-rules interface stubs ====

func (m *mockHealth) ListFailureRules(ctx context.Context) ([]failure.Rule, error) { return nil, nil }
func (m *mockHealth) CreateFailureRule(ctx context.Context, in failure.RuleInput) (failure.Rule, error) { return failure.Rule{}, nil }
func (m *mockHealth) UpdateFailureRule(ctx context.Context, id string, in failure.RuleInput) (failure.Rule, error) { return failure.Rule{}, nil }
func (m *mockHealth) DeleteFailureRule(ctx context.Context, id string) error { return nil }
func (m *mockHealth) SetFailureRuleEnabled(ctx context.Context, id string, enabled bool) error { return nil }
func (m *mockHealth) ConfirmFailureRule(ctx context.Context, id string) error { return nil }
func (m *mockHealth) VerifyFailureRule(rule failure.Rule, ev failure.Evidence) bool { return false }
func (m *mockHealth) ListRuleDecisions(ctx context.Context, limit int) ([]map[string]any, error) { return nil, nil }
func (m *mockHealth) SetRuleAIModel(model string) {}
"""
if "ListFailureRules" not in src:
    src = src + "\n" + stub
    imp_old = "\t\"loadout/plugins/contracts\""
    src = src.replace(imp_old, imp_old + "\n\tfailure \"loadout/plugins/failure-rules\"", 1)
    p.write_text(src, encoding="utf-8")
    print("stub added")
else:
    print("exists")
