# Tracewell

A lightweight observability platform for LLM applications, written in Go.

Tracewell receives trace data from LLM applications via the OpenTelemetry protocol (OTLP),
interprets it through OpenInference-style semantic conventions (span kinds, prompts,
completions, token usage), stores it, and serves query APIs for debugging latency, cost,
and quality issues in LLM-powered systems.

## How It Works

```
┌──────────────────────┐        OTLP (gRPC / HTTP)        ┌──────────────────┐
│   LLM Application    │ ──── side-channel reporting ───→ │    Tracewell     │
│ + OTel instrumentation│                                 │     server       │
└──────────────────────┘                                 └──────────────────┘
```

- The application talks directly to model providers; Tracewell never proxies LLM traffic.
- Instrumentation wraps LLM calls and reports spans asynchronously, adding no latency.
- If Tracewell is down, applications keep working — only observability data is lost.

## Development

```bash
# coming with M1
```
