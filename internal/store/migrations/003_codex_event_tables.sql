-- @author Kurok1 <im.kurokyhanc@gmail.com>
-- @since v2.2.0
-- 6 张 Codex 事件表（v1 核心用量）。Schema source: docs/superpowers/specs/2026-07-01-codex-otel-support-design.md §4。
-- 公共列约定：无 user_id NOT NULL 约束（Codex 身份字段天然可空）。

CREATE TABLE codex_event_conversation_starts (
    ts                        TIMESTAMP NOT NULL,
    received_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id           VARCHAR,
    app_version               VARCHAR,
    auth_mode                 VARCHAR,
    originator                VARCHAR,
    terminal_type             VARCHAR,
    model                     VARCHAR,
    slug                      VARCHAR,
    user_account_id           VARCHAR,
    user_email                VARCHAR,
    provider_name             VARCHAR,
    reasoning_effort          VARCHAR,
    reasoning_summary         VARCHAR,
    context_window            BIGINT,
    auto_compact_token_limit  BIGINT,
    approval_policy           VARCHAR,
    sandbox_policy            VARCHAR,
    mcp_servers               VARCHAR,
    attrs                     VARCHAR
);

CREATE TABLE codex_event_api_request (
    ts               TIMESTAMP NOT NULL,
    received_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id  VARCHAR,
    app_version      VARCHAR,
    auth_mode        VARCHAR,
    originator       VARCHAR,
    terminal_type    VARCHAR,
    model            VARCHAR,
    slug             VARCHAR,
    user_account_id  VARCHAR,
    user_email       VARCHAR,
    duration_ms      BIGINT,
    status_code      INTEGER,
    error            VARCHAR,
    attempt          BIGINT,
    endpoint         VARCHAR,
    attrs            VARCHAR
);

CREATE TABLE codex_event_token_usage (
    ts                      TIMESTAMP NOT NULL,
    received_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id         VARCHAR,
    app_version             VARCHAR,
    auth_mode               VARCHAR,
    originator              VARCHAR,
    terminal_type           VARCHAR,
    model                   VARCHAR,
    slug                    VARCHAR,
    user_account_id         VARCHAR,
    user_email              VARCHAR,
    input_token_count       BIGINT,
    output_token_count      BIGINT,
    cached_token_count      BIGINT,
    reasoning_token_count   BIGINT,
    tool_token_count        BIGINT,
    service_tier            VARCHAR,
    model_reasoning_effort  VARCHAR,
    duration_ms             BIGINT,
    attrs                   VARCHAR
);

CREATE TABLE codex_event_user_prompt (
    ts               TIMESTAMP NOT NULL,
    received_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id  VARCHAR,
    app_version      VARCHAR,
    auth_mode        VARCHAR,
    originator       VARCHAR,
    terminal_type    VARCHAR,
    model            VARCHAR,
    slug             VARCHAR,
    user_account_id  VARCHAR,
    user_email       VARCHAR,
    prompt_length    INTEGER,
    prompt           VARCHAR,
    attrs            VARCHAR
);

CREATE TABLE codex_event_tool_decision (
    ts               TIMESTAMP NOT NULL,
    received_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id  VARCHAR,
    app_version      VARCHAR,
    auth_mode        VARCHAR,
    originator       VARCHAR,
    terminal_type    VARCHAR,
    model            VARCHAR,
    slug             VARCHAR,
    user_account_id  VARCHAR,
    user_email       VARCHAR,
    tool_name        VARCHAR,
    call_id          VARCHAR,
    decision         VARCHAR,
    source           VARCHAR,
    attrs            VARCHAR
);

CREATE TABLE codex_event_tool_result (
    ts                 TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    conversation_id    VARCHAR,
    app_version        VARCHAR,
    auth_mode          VARCHAR,
    originator         VARCHAR,
    terminal_type      VARCHAR,
    model              VARCHAR,
    slug               VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    tool_name          VARCHAR,
    call_id            VARCHAR,
    duration_ms        BIGINT,
    success            BOOLEAN,
    mcp_server         VARCHAR,
    mcp_server_origin  VARCHAR,
    arguments_length   BIGINT,
    output_length      BIGINT,
    attrs              VARCHAR
);
