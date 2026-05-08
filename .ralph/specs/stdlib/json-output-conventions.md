# stdlib — JSON Output Conventions (Go)

Every Bedrock call uses **tool use** for structured output. Free-text JSON parsing is forbidden.

## Why

- Tool use forces the model into the schema's shape.
- Validation failures happen at the API boundary, not deep in business logic.
- Schemas live as Go structs in `lambdas/pkg/schemas/`; JSON Schema is generated from those structs at build time so the runtime payload and the validation can never drift.

## The wrapper

```go
// lambdas/pkg/bedrock/structured.go
package bedrock

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"

    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

    "github.com/<org>/<project>/lambdas/pkg/cost"
    "github.com/<org>/<project>/lambdas/pkg/schemas"
)

type Stage string

const (
    StageAudit          Stage = "audit"
    StageSpec           Stage = "spec"
    StageEmail          Stage = "email"
    StageTunerStyle     Stage = "tuner-style"
    StageTunerEmailTone Stage = "tuner-email-tone"
    StageTunerTargeting Stage = "tuner-targeting"
)

type InvokeInput[T any] struct {
    ModelID     string             // e.g. "anthropic.claude-haiku-4-5"
    PromptID    string             // e.g. "spec.v1"
    System      string
    Messages    []Message          // user/assistant turns
    ToolName    string             // forced; e.g. "produceSpec"
    ToolSchema  json.RawMessage    // generated from the Go struct via schemas.JSONSchemaFor[T]()
    Stage       Stage
    EstimateUSD float64
    CacheKey    string             // sha256(promptID + inputHash) — caller computes
    MaxTokens   int
    Temperature *float64
}

func InvokeStructured[T any](ctx context.Context, in InvokeInput[T]) (T, error) {
    var zero T

    // 1. Cache hit?
    if cached, ok, err := getCached[T](ctx, in.PromptID, in.CacheKey); err != nil {
        return zero, err
    } else if ok {
        return cached, nil
    }

    // 2. Cost cap.
    if err := cost.Assert(ctx, string(in.Stage), in.EstimateUSD); err != nil {
        return zero, err
    }

    // 3. Invoke.
    body, err := buildBody(in)
    if err != nil {
        return zero, err
    }

    res, err := client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
        ModelId:     aws.String(in.ModelID),
        Body:        body,
        ContentType: aws.String("application/json"),
        Accept:      aws.String("application/json"),
    })
    if err != nil {
        return zero, fmt.Errorf("bedrock invoke %s: %w", in.PromptID, err)
    }

    // 4. Extract tool_use input.
    var resp struct {
        Content []struct {
            Type  string          `json:"type"`
            Name  string          `json:"name"`
            Input json.RawMessage `json:"input"`
        } `json:"content"`
        Usage struct {
            InputTokens  int `json:"input_tokens"`
            OutputTokens int `json:"output_tokens"`
        } `json:"usage"`
    }
    if err := json.Unmarshal(res.Body, &resp); err != nil {
        return zero, err
    }
    var toolInput json.RawMessage
    for _, c := range resp.Content {
        if c.Type == "tool_use" && c.Name == in.ToolName {
            toolInput = c.Input
            break
        }
    }
    if len(toolInput) == 0 {
        return zero, fmt.Errorf("bedrock %s: no tool_use(%s) in response", in.PromptID, in.ToolName)
    }

    // 5. Validate.
    var out T
    if err := schemas.Validate(in.ToolSchema, toolInput); err != nil {
        return zero, fmt.Errorf("bedrock %s: tool input failed schema: %w", in.PromptID, err)
    }
    if err := json.Unmarshal(toolInput, &out); err != nil {
        return zero, err
    }

    // 6. Record spend + cache.
    usd := computeUSD(in.ModelID, resp.Usage.InputTokens, resp.Usage.OutputTokens)
    if err := cost.Record(ctx, string(in.Stage), usd); err != nil {
        return zero, err
    }
    if err := setCached(ctx, in.PromptID, in.CacheKey, out, usd); err != nil {
        return zero, err
    }

    return out, nil
}

func cacheKey(promptID, inputHash string) string {
    h := sha256.Sum256([]byte(promptID + ":" + inputHash))
    return hex.EncodeToString(h[:])
}

var ErrNoToolUse = errors.New("bedrock: response missing required tool_use")
```

## buildBody

Forces tool use:

```go
func buildBody[T any](in InvokeInput[T]) ([]byte, error) {
    temp := 0.2
    if in.Temperature != nil {
        temp = *in.Temperature
    }
    return json.Marshal(map[string]any{
        "anthropic_version": "bedrock-2023-05-31",
        "max_tokens":        in.MaxTokens,
        "temperature":       temp,
        "top_p":             0.9,
        "system":            in.System,
        "messages":          in.Messages,
        "tools": []map[string]any{{
            "name":         in.ToolName,
            "description":  "Single allowed output channel.",
            "input_schema": in.ToolSchema,
        }},
        "tool_choice": map[string]any{
            "type": "tool",
            "name": in.ToolName,
        },
    })
}
```

## Schema-first authoring

Schemas are Go structs with JSON tags + `jsonschema` tags. Generation uses `github.com/invopop/jsonschema`:

```go
// lambdas/pkg/schemas/spec_v1.go
package schemas

type SpecV1 struct {
    Brand       Brand       `json:"brand" jsonschema:"required"`
    Page        Page        `json:"page" jsonschema:"required"`
    SEO         SEO         `json:"seo" jsonschema:"required"`
    Constraints Constraints `json:"constraints" jsonschema:"required"`
}

type Brand struct {
    Tone        string  `json:"tone" jsonschema:"required,maxLength=200"`
    Palette     Palette `json:"palette" jsonschema:"required"`
    Positioning string  `json:"positioning" jsonschema:"required,maxLength=200"`
}

// ... etc
```

`schemas.JSONSchemaFor[T any]() json.RawMessage` reflects on `T` to produce the JSON Schema. The result is a `json.RawMessage` you pass into `InvokeStructured`. CI tests round-trip a known-good fixture through both the schema and the struct.

## Conventions

1. **Schemas in two places, generated once.** Go struct → JSON Schema via `invopop/jsonschema`. The build fails on a round-trip drift between struct and schema.
2. **Always set `tool_choice` to force the named tool.** Bedrock will try to "be helpful" otherwise.
3. **Always set `temperature: 0.2` unless documented otherwise.** Specs and emails benefit from low randomness.
4. **Always set `max_tokens` per prompt.** No global default.
5. **Always cache by `(promptID, sha256(input))`.** Cache TTL per prompt; see `07-bedrock-prompts.md`.

## Error handling

| Error | What to do |
|---|---|
| Tool-use missing in response | Retry once; on second failure, return `ErrNoToolUse`; handler routes to DLQ. |
| Schema validation fails | Return error; handler logs the validation issue and routes to DLQ. Don't silently fix bad output. |
| Bedrock 5xx | The Bedrock SDK retries. Wrap with `aws-sdk-go-v2/aws/retry.NewStandard` if needed; cap at 3 attempts. |
| Cost cap exceeded | `cost.Assert` returns `cost.ErrBudgetExceeded`; handler pauses stage and exits success (cap-paused, not error). |
| `pipelineEnabled=false` | Killswitch returns false at handler entry — the handler exits before reaching this wrapper. |

## Snapshot tests

For every prompt:
- One snapshot test for the assembled `body` (system + messages + tool schema) under fixed input. Changing a prompt requires consciously updating the snapshot. CI fails on undeliberate drift.
- One contract test on a known-good fixture response.
- One adversarial test: confirms the post-validator rejects a fake-testimonial response, a >200-word email, etc.

## Forbidden patterns

- Parsing a free-text response with a regex.
- Using `tool_choice: { "type": "auto" }`.
- Calling Bedrock without going through `InvokeStructured`.
- Constructing a `bedrockruntime.Client` outside `lambdas/pkg/bedrock/`.
