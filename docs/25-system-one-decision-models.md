# System 1 Decision Models API Specification & Reference

This document provides the authoritative API documentation, request/response schemas, type contracts, and runtime usage for System 1 Decision Models integrated into GoClaw:
1. **OpenAI Decisions API** (`gpt-6-luna`)
2. **Cloudflare Workers AI Clef** (`@cf/cloudflare/clef-flash` and `@cf/cloudflare/clef`)
3. **TypeSafe AI Jev** (`typesafe/jev-1.14` and `jev-latest`)

Unlike traditional generative LLMs (System 2) that autoregressively stream tokens and require markdown/JSON repair parsers, System 1 decision models evaluate a given state against typed question schemas in a single forward pass, returning calibrated probabilities with zero completion tokens and sub-50ms latency.

---

## 1. OpenAI Decisions API

### Overview
- **Endpoint**: `POST /v1/decisions`
- **Default Model**: `gpt-6-luna`
- **Authentication**: `Authorization: Bearer <OPENAI_API_KEY>`
- **Content-Type**: `application/json`
- **Supported Question Types**:
  - `predicate`: Evaluates binary condition (true/false) probability (0.0 to 1.0).
  - `choice`: Multi-class classification from a discrete list of `choices`.
  - `score`: Ordered rubric evaluation over graded `levels`.

### Request Schema
```json
{
  "model": "gpt-6-luna",
  "input": "User message or context state string",
  "questions": [
    {
      "name": "intent",
      "type": "choice",
      "instructions": "Classify the user intent while an assistant is executing a task",
      "choices": [
        {"value": "cancel", "description": "User wants to stop/cancel current execution"},
        {"value": "new_task", "description": "User initiates an unrelated new request"},
        {"value": "status_query", "description": "User asks for progress or current state"},
        {"value": "steer", "description": "User provides corrections or adjustments"}
      ]
    },
    {
      "name": "is_urgent",
      "type": "predicate",
      "instructions": "Does this request require immediate interruption?"
    },
    {
      "name": "risk_level",
      "type": "score",
      "instructions": "Rate potential risk level",
      "levels": [
        {"label": "low", "description": "Read-only operations with no destructive impact"},
        {"label": "medium", "description": "Modifications with easily reversible state"},
        {"label": "high", "description": "Significant changes to codebase or external resources"},
        {"label": "critical", "description": "Production credentials or dangerous actions"}
      ]
    }
  ]
}
```

### Response Schema
```json
{
  "id": "dec_6luna_8a9b0c1d2e3f",
  "object": "decisions.response",
  "model": "gpt-6-luna",
  "answers": [
    {
      "name": "intent",
      "type": "choice",
      "choice": "cancel",
      "confidence": 0.97,
      "probabilities": [
        {"value": "cancel", "probability": 0.97},
        {"value": "status_query", "probability": 0.02},
        {"value": "steer", "probability": 0.01},
        {"value": "new_task", "probability": 0.00}
      ]
    },
    {
      "name": "is_urgent",
      "type": "predicate",
      "probability": 0.92
    },
    {
      "name": "risk_level",
      "type": "score",
      "score": 2.74,
      "confidence": 0.89,
      "probabilities": [
        {"label": "low", "probability": 0.01},
        {"label": "medium", "probability": 0.10},
        {"label": "high", "probability": 0.04},
        {"label": "critical", "probability": 0.85}
      ]
    }
  ],
  "usage": {
    "input_tokens": 142,
    "output_tokens": 0,
    "total_tokens": 142
  }
}
```

---

## 2. Cloudflare Workers AI Clef

### Overview
- **Endpoint**: `POST https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/run/@cf/cloudflare/clef-flash`
- **Default Models**:
  - Full URL model: `@cf/cloudflare/clef-flash` (or `@cf/cloudflare/clef`)
  - Request body model selector: `clef-flash` (or `clef`)
- **Authentication**: `Authorization: Bearer <CLOUDFLARE_AUTH_TOKEN>`
- **Content-Type**: `application/json`
- **Supported Question Types**:
  - `noul`: Binary proposition probability.
  - `choice`: Categorical choice with `criteria` dictionary.
  - `score`: Numerical rating scale with `criteria` list.

### Request Schema
```json
{
  "model": "clef-flash",
  "state": "User message or document context state string",
  "questions": {
    "intent": {
      "type": "choice",
      "instructions": "Classify user intent",
      "criteria": {
        "status_query": "Asking about progress or current task",
        "cancel": "Asking to stop or abort",
        "steer": "Adding instructions or redirecting",
        "new_task": "A new unrelated request"
      }
    },
    "urgent": {
      "type": "noul",
      "instructions": "Is this urgent?"
    },
    "risk": {
      "type": "score",
      "instructions": "Risk rubric",
      "criteria": ["low", "medium", "high", "critical"]
    }
  }
}
```

### Response Schema
```json
{
  "success": true,
  "errors": [],
  "messages": [],
  "result": {
    "answers": {
      "intent": {
        "choice": "status_query",
        "confidence": 0.9612,
        "probabilities": {
          "status_query": 0.9612,
          "cancel": 0.0210,
          "steer": 0.0125,
          "new_task": 0.0053
        }
      },
      "urgent": {
        "noul": 0.1500
      },
      "risk": {
        "score": 1.25,
        "probabilities": {
          "low": 0.85,
          "medium": 0.10,
          "high": 0.04,
          "critical": 0.01
        }
      }
    },
    "usage": {
      "prompt_tokens": 42,
      "completion_tokens": 0
    }
  }
}
```

---

## 3. TypeSafe AI Jev

### Overview
- **Primary Native Endpoint**: `POST https://api.typesafe.ai/v1/systemone`
- **Default Model**: `typesafe-ai/jev` or `jev-latest`
- **Authentication**: `Authorization: Bearer <JEV_API_KEY>`
- **Content-Type**: `application/json`
- **Supported Question Types**:
  - `choice`: Discrete classification with `criteria` map or slice.
  - `noul`: Binary proposition probability (native TypeSafe signature primitive).
  - `score`: Gradient rating with rubric legend.
- **Vercel AI Gateway Compatibility**:
  - GoClaw automatically detects Vercel base URLs (`ai-gateway.vercel.sh` or `api.vercel.com`), adapting the path to `POST /v1/evaluate` and mapping `noul` to `boolean` to comply with Vercel's HTTP API specification.

### Request Schema
```json
{
  "model": "jev-latest",
  "state": "User message or context state string",
  "questions": {
    "intent": {
      "type": "choice",
      "instructions": "Classify intent",
      "criteria": {
        "status_query": "Check status",
        "cancel": "Abort task",
        "steer": "Add instruction"
      }
    },
    "is_blocking": {
      "type": "noul",
      "instructions": "Is this a blocking issue?"
    },
    "severity": {
      "type": "score",
      "instructions": "Rate severity",
      "criteria": ["low", "medium", "high"]
    }
  }
}
```

### Response Schema
```json
{
  "model": "jev-1.14.0",
  "provider": "typesafe",
  "request_id": "req_jev_99182",
  "answers": {
    "intent": {
      "type": "choice",
      "choice": "cancel",
      "confidence": 0.94,
      "probabilities": {
        "cancel": 0.94,
        "status_query": 0.04,
        "steer": 0.02
      }
    },
    "is_blocking": {
      "type": "noul",
      "noul": 0.98
    },
    "severity": {
      "type": "score",
      "score": 2.1,
      "confidence": 0.88,
      "probabilities": {
        "0": 0.02,
        "1": 0.10,
        "2": 0.88
      }
    }
  },
  "usage": {
    "input_tokens": 85,
    "output_tokens": 0
  }
}
```

---

## 4. GoClaw Unified Type Contracts (`internal/providers/decision.go`)

GoClaw provides a unified interface across all three providers:

```go
type DecisionProvider interface {
	Name() string
	DefaultModel() string
	Evaluate(ctx context.Context, req DecisionRequest) (*DecisionResponse, error)
}
```

### Question Types
- `QuestionTypeChoice` = `"choice"`
- `QuestionTypeScore`  = `"score"`
- `QuestionTypeNoul`   = `"noul"` (auto-mapped to `"predicate"` for OpenAI)

### Answer Types
- `ChoiceAnswer`: contains `Choice` (`string`), `Selected` (`string` fallback), `Confidence` (`float64`), `Probabilities` (`map[string]float64`).
  - Method `Value() string` returns the top chosen string.
  - Custom `UnmarshalJSON` seamlessly decodes `choice`, `selected`, and `value`.
- `ScoreAnswer`: contains `Score` (`float64`), `Confidence` (`float64`), `Probabilities` (`map[string]float64`).
- `NoulAnswer`: contains `Probability` (`float64`).

### Provider Registration & Tenant Resolution

Decision providers in GoClaw are purely database-backed and configured through the Dashboard (System Settings & Providers) on a per-tenant basis:
1. **No Environment Variables**: Decision providers do not rely on global ENV variables (`GOCLAW_DECISION_PROVIDER` or ENV API keys). All provider configs, API bases, and credentials reside securely in `llm_providers` with DB encryption.
2. **Explicit Opt-in via System Configs**: A tenant opts in by setting `decision.provider` in `system_configs` (via Dashboard System Settings modal or `PUT /v1/system-configs/decision.provider`).
3. **Resolution**: `providerresolve.ResolveDecisionProvider(ctx, tenantID, registry, systemConfigs)` resolves the configured provider by exact name in the tenant's registry namespace (or shared master provider). If `decision.provider` is not set, empty, disabled, or not found, `ResolveDecisionProvider` returns `nil`.

### Orchestration & Fallback Cascades

1. **Intent Classification**:
   - Total deadline: **10 seconds** encompassing keyword matching, Decision evaluation, and chat fallback.
   - Decision sub-timeout: capped at **2 seconds** (`context.WithTimeout(ctx, 2*time.Second)`).
   - If the Decision evaluation succeeds with sufficient confidence (threshold ≥ 0.6), the classified intent is returned immediately.
   - If Decision times out, fails, or produces low confidence, execution falls back to the agent's generative chat model using the remaining time in the 10s budget.
   - If usage cap is exceeded (`ErrCapExceeded`) or the parent request context is canceled, execution terminates immediately without chat fallback.
2. **Vault Link Enrichment**:
   - Resolved once per enrichment batch via `providerresolve.ResolveDecisionProvider`.
   - Evaluated in chunks of candidate documents.
   - All-or-nothing chunking: if `len(results) != len(chunk)` or an error occurs, the entire chunk falls back to the generative chat parser/retry flow. Partial results are never committed.
3. **Usage Caps & Billing**:
   - Zero completion tokens (`output_tokens: 0`) is valid and preserved in both preflight estimation and post-call reconcile.
   - Upstream timeouts record input tokens with status marked as `uncertain_timeout`.
