package pg

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestPGTracingStoreBackfillLLMCosts(t *testing.T) {
	db := hooksTestDB(t)
	tenantID, agentID := seedTenantAndAgent(t, db)
	providerID := uuid.Must(uuid.NewV7())
	traceID := uuid.Must(uuid.NewV7())
	spanID := uuid.Must(uuid.NewV7())
	toolSpanID := uuid.Must(uuid.NewV7())
	start := time.Date(2026, 7, 3, 1, 23, 0, 0, time.UTC)

	t.Cleanup(func() {
		db.Exec(`DELETE FROM spans WHERE id IN ($1,$2)`, spanID, toolSpanID)
		db.Exec(`DELETE FROM traces WHERE id=$1`, traceID)
		db.Exec(`DELETE FROM llm_providers WHERE id=$1`, providerID)
		db.Exec(`DELETE FROM usage_pricing_catalog WHERE model_id=$1`, "qwen/qwen3.7-plus")
	})

	if _, err := db.Exec(
		`INSERT INTO llm_providers (id, tenant_id, name, provider_type, api_key, enabled)
		 VALUES ($1,$2,'bailian','bailian','test',true)`,
		providerID, tenantID,
	); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	inputPrice := "0.000001"
	outputPrice := "0.000002"
	cacheReadPrice := "0.0000001"
	cacheWritePrice := "0.0000002"
	if _, err := NewPGUsageCapStore(db).UpsertPricingCatalog(context.Background(), []store.UsagePricingCatalogEntry{{
		ModelID:          "qwen/qwen3.7-plus",
		CanonicalModelID: "qwen/qwen3.7-plus",
		Pricing: store.UsagePricingFields{
			Input:      &inputPrice,
			Output:     &outputPrice,
			CacheRead:  &cacheReadPrice,
			CacheWrite: &cacheWritePrice,
		},
		SyncedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("upsert pricing catalog: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO traces (id, tenant_id, agent_id, start_time, created_at, status)
		 VALUES ($1,$2,$3,$4,$4,'completed')`,
		traceID, tenantID, agentID, start,
	); err != nil {
		t.Fatalf("insert trace: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO spans (
			id, trace_id, tenant_id, agent_id, span_type, start_time, created_at, status,
			provider, model, input_tokens, output_tokens, total_cost, metadata
		) VALUES ($1,$2,$3,$4,'llm_call',$5,$5,'completed','bailian','qwen3.7-plus',1000,500,0,$6::jsonb)`,
		spanID, traceID, tenantID, agentID, start,
		`{"cache_read_tokens":10,"cache_creation_tokens":5,"thinking_tokens":100}`,
	); err != nil {
		t.Fatalf("insert span: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO spans (
			id, trace_id, tenant_id, agent_id, span_type, start_time, created_at, status,
			name, provider, model, input_tokens, output_tokens, total_cost
		) VALUES ($1,$2,$3,$4,'tool_call',$5,$5,'completed','read_image','bailian','qwen3.7-plus',100,50,0)`,
		toolSpanID, traceID, tenantID, agentID, start,
	); err != nil {
		t.Fatalf("insert tool span: %v", err)
	}

	stats, err := NewPGTracingStore(db).BackfillLLMCosts(context.Background())
	if err != nil {
		t.Fatalf("BackfillLLMCosts: %v", err)
	}
	if stats.SpanRowsUpdated == 0 || stats.TraceRowsUpdated == 0 || len(stats.SnapshotBuckets) == 0 {
		t.Fatalf("stats = %+v, want updated span, trace, and snapshot bucket", stats)
	}

	var spanCost, toolSpanCost, traceCost float64
	if err := db.QueryRow(`SELECT total_cost FROM spans WHERE id=$1`, spanID).Scan(&spanCost); err != nil {
		t.Fatalf("query span cost: %v", err)
	}
	if err := db.QueryRow(`SELECT total_cost FROM spans WHERE id=$1`, toolSpanID).Scan(&toolSpanCost); err != nil {
		t.Fatalf("query tool span cost: %v", err)
	}
	if err := db.QueryRow(`SELECT total_cost FROM traces WHERE id=$1`, traceID).Scan(&traceCost); err != nil {
		t.Fatalf("query trace cost: %v", err)
	}
	if spanCost <= 0 || traceCost <= 0 {
		t.Fatalf("costs = span %.8f trace %.8f, want positive", spanCost, traceCost)
	}
	if math.Abs(spanCost-0.001987) > 0.0000001 {
		t.Fatalf("span cost = %.8f, want 0.001987", spanCost)
	}
	if math.Abs(toolSpanCost-0.0002) > 0.0000001 {
		t.Fatalf("tool span cost = %.8f, want 0.0002", toolSpanCost)
	}
	if math.Abs(traceCost-0.002187) > 0.0000001 {
		t.Fatalf("trace cost = %.8f, want 0.002187", traceCost)
	}

	var inputTokens, outputTokens, llmCalls, toolCalls int
	if err := db.QueryRow(`SELECT total_input_tokens, total_output_tokens, llm_call_count, tool_call_count FROM traces WHERE id=$1`, traceID).Scan(&inputTokens, &outputTokens, &llmCalls, &toolCalls); err != nil {
		t.Fatalf("query trace aggregates: %v", err)
	}
	if inputTokens != 1100 || outputTokens != 550 || llmCalls != 1 || toolCalls != 1 {
		t.Fatalf("trace aggregates = input %d output %d llm %d tool %d, want 1100/550/1/1", inputTokens, outputTokens, llmCalls, toolCalls)
	}
}

func TestPGTracingStoreReconcileTraceUsageAggregatesIncludesToolUsageTokens(t *testing.T) {
	db := hooksTestDB(t)
	tenantID, agentID := seedTenantAndAgent(t, db)
	traceID := uuid.Must(uuid.NewV7())
	llmSpanID := uuid.Must(uuid.NewV7())
	toolSpanID := uuid.Must(uuid.NewV7())
	start := time.Date(2026, 7, 3, 2, 0, 0, 0, time.UTC)

	t.Cleanup(func() {
		db.Exec(`DELETE FROM spans WHERE id IN ($1,$2)`, llmSpanID, toolSpanID)
		db.Exec(`DELETE FROM traces WHERE id=$1`, traceID)
	})

	if _, err := db.Exec(
		`INSERT INTO traces (
			id, tenant_id, agent_id, start_time, created_at, status,
			total_input_tokens, total_output_tokens, total_cost,
			span_count, llm_call_count, tool_call_count
		) VALUES ($1,$2,$3,$4,$4,'completed',1000,200,0.25,1,1,0)`,
		traceID, tenantID, agentID, start,
	); err != nil {
		t.Fatalf("insert trace: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO spans (
			id, trace_id, tenant_id, agent_id, span_type, start_time, created_at, status,
			provider, model, input_tokens, output_tokens, total_cost
		) VALUES
			($1,$3,$4,$5,'llm_call',$6,$6,'completed','bailian','qwen3.7-plus',1000,200,0.25),
			($2,$3,$4,$5,'tool_call',$6,$6,'completed','bailian','qwen3.7-plus',100,50,0.05)`,
		llmSpanID, toolSpanID, traceID, tenantID, agentID, start,
	); err != nil {
		t.Fatalf("insert spans: %v", err)
	}

	stats, err := NewPGTracingStore(db).ReconcileTraceUsageAggregates(context.Background())
	if err != nil {
		t.Fatalf("ReconcileTraceUsageAggregates: %v", err)
	}
	if stats.TraceRowsUpdated == 0 {
		t.Fatalf("stats = %+v, want updated trace", stats)
	}

	var inputTokens, outputTokens, llmCalls, toolCalls, spanCount int
	var traceCost float64
	if err := db.QueryRow(`
		SELECT total_input_tokens, total_output_tokens, total_cost, span_count, llm_call_count, tool_call_count
		FROM traces WHERE id=$1`, traceID).Scan(&inputTokens, &outputTokens, &traceCost, &spanCount, &llmCalls, &toolCalls); err != nil {
		t.Fatalf("query trace aggregates: %v", err)
	}
	if inputTokens != 1100 || outputTokens != 250 || math.Abs(traceCost-0.30) > 0.0000001 || spanCount != 2 || llmCalls != 1 || toolCalls != 1 {
		t.Fatalf("trace aggregates = input %d output %d cost %.8f spans %d llm %d tool %d, want 1100/250/0.30/2/1/1",
			inputTokens, outputTokens, traceCost, spanCount, llmCalls, toolCalls)
	}
}

// A claude-cli wrapper llm_call span carries provider_run metadata: it is
// not an API call (left out of llm_call_count) and its zero remainder cost is
// real (the CLI reported the run cost), so backfill must not price it.
func TestPGTracingStoreProviderRunWrapperNotCountedOrBackfilled(t *testing.T) {
	db := hooksTestDB(t)
	tenantID, agentID := seedTenantAndAgent(t, db)
	traceID := uuid.Must(uuid.NewV7())
	wrapperID := uuid.Must(uuid.NewV7())
	childID := uuid.Must(uuid.NewV7())
	start := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	model := "claude-wrapper-test-model"

	t.Cleanup(func() {
		db.Exec(`DELETE FROM spans WHERE id IN ($1,$2)`, wrapperID, childID)
		db.Exec(`DELETE FROM traces WHERE id=$1`, traceID)
		db.Exec(`DELETE FROM usage_pricing_catalog WHERE model_id=$1`, "anthropic/"+model)
	})

	price := "0.00001"
	if _, err := NewPGUsageCapStore(db).UpsertPricingCatalog(context.Background(), []store.UsagePricingCatalogEntry{{
		ModelID:          "anthropic/" + model,
		CanonicalModelID: "anthropic/" + model,
		Pricing:          store.UsagePricingFields{Input: &price, Output: &price, CacheRead: &price, CacheWrite: &price},
		SyncedAt:         time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("upsert pricing catalog: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO traces (id, tenant_id, agent_id, start_time, created_at, status)
		 VALUES ($1,$2,$3,$4,$4,'completed')`,
		traceID, tenantID, agentID, start,
	); err != nil {
		t.Fatalf("insert trace: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO spans (
			id, trace_id, parent_span_id, tenant_id, agent_id, span_type, start_time, created_at, status,
			provider, model, input_tokens, output_tokens, total_cost, metadata
		) VALUES
			($1,$3,NULL,$4,$5,'llm_call',$6,$6,'completed','anthropic',$7,0,400,0,
			 '{"provider_run":{"nested_llm_calls":1,"nested_tool_calls":0,"cost_usd":0.5}}'::jsonb),
			($2,$3,$1,$4,$5,'llm_call',$6,$6,'completed','anthropic',$7,10,25,0.5,
			 '{"nested_source":"claude_cli","cache_read_tokens":1000}'::jsonb)`,
		wrapperID, childID, traceID, tenantID, agentID, start, model,
	); err != nil {
		t.Fatalf("insert spans: %v", err)
	}

	ts := NewPGTracingStore(db)
	if _, err := ts.BackfillLLMCosts(context.Background()); err != nil {
		t.Fatalf("BackfillLLMCosts: %v", err)
	}
	var wrapperCost float64
	if err := db.QueryRow(`SELECT COALESCE(total_cost,0) FROM spans WHERE id=$1`, wrapperID).Scan(&wrapperCost); err != nil {
		t.Fatalf("query wrapper cost: %v", err)
	}
	if wrapperCost != 0 {
		t.Fatalf("wrapper span was backfilled: cost %.8f", wrapperCost)
	}

	check := func(label string) {
		t.Helper()
		var llmCalls, outputTokens int
		var cost float64
		if err := db.QueryRow(`SELECT llm_call_count, total_output_tokens, total_cost FROM traces WHERE id=$1`, traceID).Scan(&llmCalls, &outputTokens, &cost); err != nil {
			t.Fatalf("%s: query trace: %v", label, err)
		}
		if llmCalls != 1 || outputTokens != 425 || math.Abs(cost-0.5) > 1e-9 {
			t.Fatalf("%s: trace = llm %d output %d cost %v, want 1/425/0.5", label, llmCalls, outputTokens, cost)
		}
	}
	if err := ts.BatchUpdateTraceAggregates(context.Background(), traceID); err != nil {
		t.Fatalf("BatchUpdateTraceAggregates: %v", err)
	}
	check("batch update")
	if _, err := ts.ReconcileTraceUsageAggregates(context.Background()); err != nil {
		t.Fatalf("ReconcileTraceUsageAggregates: %v", err)
	}
	check("reconcile")
}
