-- 8 metric tables. Schema source: docs/models.md §2.

CREATE TABLE metric_session_count (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    value              BIGINT    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    start_type         VARCHAR,
    attrs              VARCHAR
);

CREATE TABLE metric_lines_of_code_count (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    value              BIGINT    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    type               VARCHAR,
    attrs              VARCHAR
);

CREATE TABLE metric_pull_request_count (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    value              BIGINT    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    attrs              VARCHAR
);

CREATE TABLE metric_commit_count (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    value              BIGINT    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    attrs              VARCHAR
);

CREATE TABLE metric_cost_usage (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    value              DOUBLE    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    model              VARCHAR,
    query_source       VARCHAR,
    speed              VARCHAR,
    effort             VARCHAR,
    agent_name         VARCHAR,
    skill_name         VARCHAR,
    plugin_name        VARCHAR,
    marketplace_name   VARCHAR,
    attrs              VARCHAR
);

CREATE TABLE metric_token_usage (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    value              BIGINT    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    type               VARCHAR,
    model              VARCHAR,
    query_source       VARCHAR,
    speed              VARCHAR,
    effort             VARCHAR,
    agent_name         VARCHAR,
    skill_name         VARCHAR,
    plugin_name        VARCHAR,
    marketplace_name   VARCHAR,
    attrs              VARCHAR
);

CREATE TABLE metric_code_edit_tool_decision (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    value              BIGINT    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    tool_name          VARCHAR,
    decision           VARCHAR,
    source             VARCHAR,
    language           VARCHAR,
    attrs              VARCHAR
);

CREATE TABLE metric_active_time_total (
    ts                 TIMESTAMP NOT NULL,
    start_ts           TIMESTAMP NOT NULL,
    received_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    value              DOUBLE    NOT NULL,
    session_id         VARCHAR,
    user_id            VARCHAR   NOT NULL,
    user_account_uuid  VARCHAR,
    user_account_id    VARCHAR,
    user_email         VARCHAR,
    organization_id    VARCHAR,
    app_version        VARCHAR,
    terminal_type      VARCHAR,
    type               VARCHAR,
    attrs              VARCHAR
);
