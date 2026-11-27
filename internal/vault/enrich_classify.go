package vault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/providerresolve"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	usagecaps "github.com/nextlevelbuilder/goclaw/internal/usage/caps"
)

const (
	classifyMaxTokens       = 4096
	classifyTemperature     = 0.1
	classifyCtxMaxLen       = 256 // max context string length stored in DB
	classifySummaryMaxChars = 300 // max summary chars in prompt (validated: 300 for accuracy)
	classifyMaxSourceDocs   = 20  // max source docs per classifyLinks call (validated: cap unbounded time)
	classifyChunkSize       = 5   // candidates per LLM call to fit response within max_tokens
)

// validClassifyTypes — accepted link types stored directly in DB (aligned with UI vault-link-dialog.tsx).
var validClassifyTypes = map[string]bool{
	"reference": true, "depends_on": true, "extends": true,
	"related": true, "supersedes": true, "contradicts": true,
}

// classifyTypeContextTemplates maps classification relationship types to readable explanation templates.
var classifyTypeContextTemplates = map[string]string{
	"reference":   "cites or mentions %s",
	"depends_on":  "requires %s to function",
	"extends":     "expands or elaborates on %s",
	"related":     "shares topic or domain with %s",
	"supersedes":  "replaces or updates %s",
	"contradicts": "conflicts with or opposes %s",
}

func buildDecisionLinkContext(relType, targetTitle, targetPath string) string {
	tmpl, ok := classifyTypeContextTemplates[relType]
	if !ok {
		if relType != "" {
			return relType
		}
		return "related"
	}
	target := strings.TrimSpace(targetTitle)
	if target == "" {
		target = strings.TrimSpace(targetPath)
	}
	if target == "" {
		target = "target document"
	} else if len([]rune(target)) > 60 {
		target = string([]rune(target)[:57]) + "..."
	}
	return fmt.Sprintf(tmpl, target)
}

type classifyDoc struct {
	DocID, Title, Path, Summary string
}

type candidatePair struct {
	Source, Candidate classifyDoc
	Score             float64
}

// classifyLinks orchestrates LLM-based link classification for enriched docs.
func (w *EnrichWorker) classifyLinks(ctx context.Context, provider providers.Provider, model, tenantID, agentID string, results []enriched) {
	if provider == nil {
		return
	}
	if tid, err := uuid.Parse(tenantID); err == nil {
		ctx = store.WithTenantID(ctx, tid)
	}
	if agentID != "" {
		if aid, err := uuid.Parse(agentID); err == nil {
			ctx = store.WithAgentID(ctx, aid)
		}
	}

	capped := results
	if len(capped) > classifyMaxSourceDocs {
		capped = capped[:classifyMaxSourceDocs]
	}

	// On SQLite (desktop/Lite), FindSimilarDocs returns nil (no pgvector) — classify is a no-op.
	candidates := w.gatherCandidates(ctx, tenantID, agentID, capped)
	if len(candidates) == 0 {
		return
	}

	allTypes := slices.Collect(maps.Keys(validClassifyTypes))
	allTypes = append(allTypes, "semantic") // clean up legacy links

	// Resolve decision provider once for the batch using tenant selection
	var dp providers.DecisionProvider
	if w.registry != nil && w.systemConfigs != nil {
		if tid, err := uuid.Parse(tenantID); err == nil {
			dp = providerresolve.ResolveDecisionProvider(ctx, tid, w.registry, w.systemConfigs)
		}
	}

	for sourceDocID, pairs := range candidates {
		source := pairs[0].Source
		allCandidates := make([]classifyDoc, len(pairs))
		for i, p := range pairs {
			allCandidates[i] = p.Candidate
		}

		// Chunk candidates to keep LLM response within max_tokens.
		var newLinks []store.VaultLink
		for chunkStart := 0; chunkStart < len(allCandidates); chunkStart += classifyChunkSize {
			chunkEnd := min(chunkStart+classifyChunkSize, len(allCandidates))
			chunk := allCandidates[chunkStart:chunkEnd]

			var parsed []classifyResult
			// Fast-path: System 1 Decision Model (Clef / Jev)
			// Must return results for all chunk candidates; if partial/missing, fall back to chat LLM for entire chunk.
			// If usage cap exceeded or context canceled, abort immediately without chat fallback.
			if dp != nil {
				start := time.Now()
				res, err := classifyCandidatesWithDecisionAndCaps(ctx, dp, w.usageCaps, source, chunk)
				durationMs := time.Since(start).Milliseconds()
				if err == nil && len(res) == len(chunk) {
					slog.Info("decision: evaluation succeeded",
						"tenant_id", tenantID,
						"purpose", "vault-classify",
						"provider", dp.Name(),
						"candidates_count", len(chunk),
						"duration_ms", durationMs)
					parsed = res
				} else if errors.Is(err, usagecaps.ErrCapExceeded) {
					slog.Warn("decision: usage cap blocked",
						"tenant_id", tenantID,
						"purpose", "vault-classify",
						"provider", dp.Name(),
						"doc", sourceDocID,
						"chunk", chunkStart,
						"duration_ms", durationMs)
					if w.progress != nil {
						w.progress.AddError(fmt.Sprintf("cap exceeded for %s: %v", sourceDocID, err))
					}
					continue
				} else if ctx.Err() != nil {
					slog.Info("decision: parent context canceled, aborting chunk",
						"tenant_id", tenantID,
						"purpose", "vault-classify",
						"provider", dp.Name(),
						"doc", sourceDocID,
						"chunk", chunkStart,
						"duration_ms", durationMs)
					continue
				} else {
					reason, cleanMsg := providerresolve.ClassifyDecisionError(err)
					if err == nil && len(res) != len(chunk) {
						reason = "invalid_response"
						cleanMsg = fmt.Sprintf("partial results: expected %d, got %d", len(chunk), len(res))
					}
					slog.Info("decision: fallback to chat",
						"tenant_id", tenantID,
						"purpose", "vault-classify",
						"provider", dp.Name(),
						"candidates_count", len(chunk),
						"reason", reason,
						"duration_ms", durationMs,
						"error", cleanMsg)
				}
			}

			// Fallback: System 2 Generative LLM with retry
			if len(parsed) == 0 {
				system, user := buildClassifyPrompt(source, chunk)
				raw, err := w.callClassifyWithRetry(ctx, provider, model, system, user)
				if err != nil {
					slog.Warn("vault.classify: llm_failed", "doc", sourceDocID, "chunk", chunkStart, "err", err)
					if w.progress != nil {
						w.progress.AddError(fmt.Sprintf("classify failed for %s: %v", sourceDocID, err))
					}
					continue
				}

				var parseErr error
				parsed, parseErr = parseClassifyResponse(raw, len(chunk))
				if parseErr != nil {
					slog.Warn("vault.classify: parse_failed_first", "doc", sourceDocID, "err", parseErr, "raw_len", len(raw), "raw", raw)
					hint := fmt.Sprintf("\n\nPrevious response was invalid JSON (error: %s). Output ONLY a valid JSON array.", parseErr.Error())
					raw2, err2 := w.callClassifyWithRetry(ctx, provider, model, system, user+hint)
					if err2 != nil {
						slog.Warn("vault.classify: retry_parse_failed", "doc", sourceDocID, "err", err2)
						continue
					}
					parsed, parseErr = parseClassifyResponse(raw2, len(chunk))
					if parseErr != nil {
						slog.Warn("vault.classify: parse_still_failed", "doc", sourceDocID, "err", parseErr, "raw_len", len(raw2), "raw", raw2)
						continue
					}
				}
			}

			for _, r := range parsed {
				if r.Type == "SKIP" || !validClassifyTypes[r.Type] {
					continue
				}
				linkCtx := r.Ctx
				if len(linkCtx) > classifyCtxMaxLen {
					linkCtx = string([]rune(linkCtx)[:classifyCtxMaxLen])
				}
				// r.Idx is 1-based within chunk; map back to original candidate.
				newLinks = append(newLinks, store.VaultLink{
					FromDocID: sourceDocID,
					ToDocID:   chunk[r.Idx-1].DocID,
					LinkType:  r.Type,
					Context:   linkCtx,
				})
			}
		}

		// Only replace old links if LLM produced valid replacements (avoid data loss on all-SKIP).
		if len(newLinks) > 0 {
			if err := w.vault.DeleteDocLinksByTypes(ctx, tenantID, sourceDocID, allTypes); err != nil {
				slog.Warn("vault.classify: delete_old", "doc", sourceDocID, "err", err)
			}
			if err := w.vault.CreateLinks(ctx, newLinks); err != nil {
				slog.Debug("vault.classify: batch_create_links", "from", sourceDocID, "count", len(newLinks), "err", err)
			}
		}
	}
}

func (w *EnrichWorker) gatherCandidates(ctx context.Context, tenantID, _ string, results []enriched) map[string][]candidatePair {
	seen := make(map[string]bool)
	out := make(map[string][]candidatePair)

	for _, r := range results {
		// Search across ALL docs in the tenant (empty agentID = no agent filter).
		// Cross-agent links are created freely; access control is enforced at
		// query time so agents only see their own docs. Pre-built cross-agent
		// links enable future vault sharing without re-enrichment.
		neighbors, err := w.vault.FindSimilarDocs(ctx, tenantID, "", r.payload.DocID, enrichSimilarityLimit)
		if err != nil {
			slog.Warn("vault.classify: find_similar", "doc", r.payload.DocID, "err", err)
			continue
		}
		// Use title carried from Phase 0 batch-fetch (avoids per-doc refetch).
		title := r.title
		if title == "" {
			title = r.payload.Path
		}
		src := classifyDoc{
			DocID:   r.payload.DocID,
			Title:   title,
			Path:    r.payload.Path,
			Summary: truncateSummary(r.summary),
		}
		for _, n := range neighbors {
			if n.Score < enrichSimilarityMin || n.Document.Summary == "" {
				continue
			}
			// Skip meaningless filenames as link targets — they create noise.
			if shouldSkipEnrichment(n.Document.PathBasename) {
				continue
			}
			// Bidirectional dedup: only process each pair once.
			a, b := src.DocID, n.Document.ID
			if a > b {
				a, b = b, a
			}
			if key := a + ":" + b; seen[key] {
				continue
			} else {
				seen[key] = true
			}
			out[src.DocID] = append(out[src.DocID], candidatePair{
				Source: src,
				Candidate: classifyDoc{
					DocID:   n.Document.ID,
					Title:   n.Document.Title,
					Path:    n.Document.Path,
					Summary: truncateSummary(n.Document.Summary),
				},
				Score: n.Score,
			})
		}
	}
	return out
}

// callClassifyWithRetry calls the LLM with shared retry logic.
func (w *EnrichWorker) callClassifyWithRetry(ctx context.Context, provider providers.Provider, model, system, user string) (string, error) {
	return w.chatWithRetry(ctx, provider, "vault.classify", providers.ChatRequest{
		Messages: []providers.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Model:   model,
		Options: map[string]any{"max_tokens": classifyMaxTokens, "temperature": classifyTemperature},
	})
}

// ErrDecisionFallback indicates the decision provider cannot confidently classify the relationship
// and the caller should fall back to generative chat LLM.
var ErrDecisionFallback = errors.New("decision fallback to chat")

// classifyCandidatesWithDecision evaluates link relationships using a System 1 DecisionProvider.
func classifyCandidatesWithDecision(ctx context.Context, decisionProv providers.DecisionProvider, source classifyDoc, candidates []classifyDoc) ([]classifyResult, bool) {
	res, err := classifyCandidatesWithDecisionAndCaps(ctx, decisionProv, nil, source, candidates)
	return res, err == nil
}

// classifyCandidatesWithDecisionAndCaps evaluates link relationships using a System 1 DecisionProvider,
// routing through usageCaps when provided to enforce budget caps and track token consumption.
// Returns (results, nil) on confident match, ErrCapExceeded on cap rejection, context errors on cancel,
// or ErrDecisionFallback if chat fallback should be attempted.
func classifyCandidatesWithDecisionAndCaps(ctx context.Context, decisionProv providers.DecisionProvider, usageCaps *usagecaps.Service, source classifyDoc, candidates []classifyDoc) ([]classifyResult, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if decisionProv == nil || len(candidates) == 0 {
		return nil, ErrDecisionFallback
	}

	questions := make(map[string]providers.DecisionQuestion, len(candidates))
	for i, c := range candidates {
		qKey := fmt.Sprintf("cand_%d", i+1)
		questions[qKey] = providers.DecisionQuestion{
			Type:         providers.QuestionTypeChoice,
			Instructions: fmt.Sprintf("Classify relationship from Source to Candidate %d: %s (%s)", i+1, c.Title, c.Summary),
			Criteria: map[string]string{
				"reference":   "Source cites, mentions, or quotes Candidate",
				"depends_on":  "Source requires Candidate to function",
				"extends":     "Source expands, details, or elaborates on Candidate",
				"related":     "Source and Candidate share same topic",
				"supersedes":  "Source is a newer version replacing Candidate",
				"contradicts": "Source conflicts with or opposes Candidate",
				"SKIP":        "No meaningful relationship exists",
			},
		}
	}

	state := fmt.Sprintf("Source Document:\nTitle: %s\nPath: %s\nSummary: %s", source.Title, source.Path, source.Summary)
	req := providers.DecisionRequest{
		State:     state,
		Questions: questions,
	}

	evalCtx, evalCancel := context.WithTimeout(ctx, 5*time.Second)
	defer evalCancel()

	var resp *providers.DecisionResponse
	var err error
	if usageCaps != nil {
		resp, err = usageCaps.Evaluate(evalCtx, decisionProv, req, usagecaps.DecisionOptions{
			Purpose: "vault-classify",
		})
	} else {
		resp, err = decisionProv.Evaluate(evalCtx, req)
	}
	if err != nil {
		if errors.Is(err, usagecaps.ErrCapExceeded) {
			return nil, err
		}
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrDecisionFallback, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("%w: nil response", ErrDecisionFallback)
	}

	results := make([]classifyResult, 0, len(candidates))
	for i := range candidates {
		qKey := fmt.Sprintf("cand_%d", i+1)
		ca, ok := resp.GetChoice(qKey)
		if !ok || ca == nil {
			return nil, fmt.Errorf("%w: missing choice %s", ErrDecisionFallback, qKey)
		}
		val := ca.Value()
		// Every candidate must receive either "SKIP" or an allowed relationship type.
		// If any label is unrecognized or missing, invalidate the chunk so it cleanly
		// falls back to the generative LLM without dropping links.
		if val != "SKIP" && !validClassifyTypes[val] {
			return nil, fmt.Errorf("%w: invalid type %q", ErrDecisionFallback, val)
		}
		// Require confident classification (>= 0.60); otherwise fall back to generative LLM
		// to avoid creating or deleting links on low certainty.
		if ca.Confidence < 0.60 {
			return nil, fmt.Errorf("%w: low confidence (%.2f < 0.60)", ErrDecisionFallback, ca.Confidence)
		}
		results = append(results, classifyResult{
			Idx:  i + 1,
			Type: val,
			Ctx:  buildDecisionLinkContext(val, candidates[i].Title, candidates[i].Path),
		})
	}
	if len(results) != len(candidates) {
		return nil, fmt.Errorf("%w: partial results (%d/%d)", ErrDecisionFallback, len(results), len(candidates))
	}
	return results, nil
}
