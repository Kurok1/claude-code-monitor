# Source this file (do NOT execute) before running `claude`:
#   source scripts/claude-env.sh
#   claude
#
# Points Claude Code's OTLP exporter at the local monitor.

export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_METRICS_EXPORTER=otlp
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317
export OTEL_METRIC_EXPORT_INTERVAL=10000
export OTEL_LOGS_EXPORT_INTERVAL=5000

# --- Optional: verbose event payloads (off by default) ---------------------
#
# Uncomment to unlock fields Claude Code redacts by default. WARNING: turning
# these on writes sensitive data straight into data/monitor.duckdb. Make sure
# you understand what gets persisted before enabling, and never share / commit
# the resulting database file.
#
#   OTEL_LOG_USER_PROMPTS=1
#     → event_user_prompt.prompt gets the raw user message text
#       (chat content, pasted snippets, credentials if you typed any, etc.)
#
#   OTEL_LOG_TOOL_DETAILS=1
#     → event_tool_result.tool_parameters / .tool_input get full tool args
#       (Bash command lines, file paths, Edit/Write content snippets)
#     → event_tool_result.error / event_mcp_server_connection.error get raw
#       error strings (may include stack traces, hostnames, tokens)
#     → event_mcp_server_connection.server_name gets the real MCP server name
#     → event_user_prompt.command_name gets the real custom-command name
#     → event_skill_activated.skill_name gets the real skill name
#
# export OTEL_LOG_USER_PROMPTS=1
# export OTEL_LOG_TOOL_DETAILS=1
