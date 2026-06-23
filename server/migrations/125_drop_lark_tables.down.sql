-- Recreate the lark_* tables dropped by 125_drop_lark_tables.up.sql
-- (structure only — the data lives in channel_* after migration 124). This is
-- the authoritative pre-drop schema: lark_installation plus the deltas from
-- migrations 112 (bot_union_id), 113 (per-installation dedup), 116 (region),
-- and 122 (thread-reply columns). Foreign keys are added after all tables
-- exist, so table order does not matter.


CREATE TABLE IF NOT EXISTS lark_binding_token (
    token_hash text NOT NULL,
    workspace_id uuid NOT NULL,
    installation_id uuid NOT NULL,
    lark_open_id text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT lark_binding_token_ttl_cap CHECK ((expires_at <= (created_at + '00:15:00'::interval)))
);

CREATE TABLE IF NOT EXISTS lark_chat_session_binding (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    chat_session_id uuid NOT NULL,
    installation_id uuid NOT NULL,
    lark_chat_id text NOT NULL,
    lark_chat_type text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_lark_message_id text,
    last_lark_thread_id text,
    CONSTRAINT lark_chat_session_binding_lark_chat_type_check CHECK ((lark_chat_type = ANY (ARRAY['p2p'::text, 'group'::text])))
);

CREATE TABLE IF NOT EXISTS lark_inbound_audit (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    installation_id uuid,
    lark_chat_id text,
    event_type text NOT NULL,
    lark_event_id text,
    lark_message_id text,
    drop_reason text NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS lark_inbound_message_dedup (
    installation_id uuid NOT NULL,
    message_id text NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    processed_at timestamp with time zone,
    claim_token uuid DEFAULT gen_random_uuid() NOT NULL
);

CREATE TABLE IF NOT EXISTS lark_installation (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    app_id text NOT NULL,
    app_secret_encrypted bytea NOT NULL,
    tenant_key text,
    bot_open_id text NOT NULL,
    installer_user_id uuid NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    ws_lease_token text,
    ws_lease_expires_at timestamp with time zone,
    installed_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    bot_union_id text,
    region text DEFAULT 'feishu'::text NOT NULL,
    CONSTRAINT lark_installation_region_check CHECK ((region = ANY (ARRAY['feishu'::text, 'lark'::text]))),
    CONSTRAINT lark_installation_status_check CHECK ((status = ANY (ARRAY['active'::text, 'revoked'::text])))
);

CREATE TABLE IF NOT EXISTS lark_outbound_card_message (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    chat_session_id uuid NOT NULL,
    task_id uuid,
    lark_chat_id text NOT NULL,
    lark_card_message_id text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    last_patched_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT lark_outbound_card_message_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'streaming'::text, 'final'::text, 'error'::text])))
);

CREATE TABLE IF NOT EXISTS lark_user_binding (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    multica_user_id uuid NOT NULL,
    installation_id uuid NOT NULL,
    lark_open_id text NOT NULL,
    union_id text,
    bound_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY lark_binding_token
    ADD CONSTRAINT lark_binding_token_pkey PRIMARY KEY (token_hash);

ALTER TABLE ONLY lark_chat_session_binding
    ADD CONSTRAINT lark_chat_session_binding_chat_session_id_key UNIQUE (chat_session_id);

ALTER TABLE ONLY lark_chat_session_binding
    ADD CONSTRAINT lark_chat_session_binding_installation_id_lark_chat_id_key UNIQUE (installation_id, lark_chat_id);

ALTER TABLE ONLY lark_chat_session_binding
    ADD CONSTRAINT lark_chat_session_binding_pkey PRIMARY KEY (id);

ALTER TABLE ONLY lark_inbound_audit
    ADD CONSTRAINT lark_inbound_audit_pkey PRIMARY KEY (id);

ALTER TABLE ONLY lark_inbound_message_dedup
    ADD CONSTRAINT lark_inbound_message_dedup_pkey PRIMARY KEY (installation_id, message_id);

ALTER TABLE ONLY lark_installation
    ADD CONSTRAINT lark_installation_app_id_key UNIQUE (app_id);

ALTER TABLE ONLY lark_installation
    ADD CONSTRAINT lark_installation_id_workspace_id_key UNIQUE (id, workspace_id);

ALTER TABLE ONLY lark_installation
    ADD CONSTRAINT lark_installation_pkey PRIMARY KEY (id);

ALTER TABLE ONLY lark_installation
    ADD CONSTRAINT lark_installation_workspace_id_agent_id_key UNIQUE (workspace_id, agent_id);

ALTER TABLE ONLY lark_outbound_card_message
    ADD CONSTRAINT lark_outbound_card_message_pkey PRIMARY KEY (id);

ALTER TABLE ONLY lark_user_binding
    ADD CONSTRAINT lark_user_binding_installation_id_lark_open_id_key UNIQUE (installation_id, lark_open_id);

ALTER TABLE ONLY lark_user_binding
    ADD CONSTRAINT lark_user_binding_pkey PRIMARY KEY (id);

CREATE INDEX IF NOT EXISTS idx_lark_binding_token_installation ON lark_binding_token USING btree (installation_id, expires_at);

CREATE INDEX IF NOT EXISTS idx_lark_chat_session_binding_session ON lark_chat_session_binding USING btree (chat_session_id);

CREATE INDEX IF NOT EXISTS idx_lark_inbound_audit_installation ON lark_inbound_audit USING btree (installation_id, received_at DESC);

CREATE INDEX IF NOT EXISTS idx_lark_inbound_audit_reason ON lark_inbound_audit USING btree (drop_reason, received_at DESC);

CREATE INDEX IF NOT EXISTS idx_lark_inbound_dedup_received ON lark_inbound_message_dedup USING btree (received_at);

CREATE INDEX IF NOT EXISTS idx_lark_installation_agent ON lark_installation USING btree (agent_id);

CREATE INDEX IF NOT EXISTS idx_lark_installation_lease ON lark_installation USING btree (ws_lease_expires_at) WHERE (status = 'active'::text);

CREATE INDEX IF NOT EXISTS idx_lark_installation_workspace ON lark_installation USING btree (workspace_id);

CREATE INDEX IF NOT EXISTS idx_lark_outbound_card_session ON lark_outbound_card_message USING btree (chat_session_id, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_lark_outbound_card_task ON lark_outbound_card_message USING btree (task_id) WHERE (task_id IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_lark_user_binding_user ON lark_user_binding USING btree (multica_user_id, workspace_id);

CREATE INDEX IF NOT EXISTS idx_lark_user_binding_workspace_open ON lark_user_binding USING btree (workspace_id, lark_open_id);

ALTER TABLE ONLY lark_binding_token
    ADD CONSTRAINT lark_binding_token_installation_id_fkey FOREIGN KEY (installation_id) REFERENCES lark_installation(id) ON DELETE CASCADE;

ALTER TABLE ONLY lark_binding_token
    ADD CONSTRAINT lark_binding_token_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY lark_chat_session_binding
    ADD CONSTRAINT lark_chat_session_binding_chat_session_id_fkey FOREIGN KEY (chat_session_id) REFERENCES chat_session(id) ON DELETE CASCADE;

ALTER TABLE ONLY lark_chat_session_binding
    ADD CONSTRAINT lark_chat_session_binding_installation_id_fkey FOREIGN KEY (installation_id) REFERENCES lark_installation(id) ON DELETE CASCADE;

ALTER TABLE ONLY lark_inbound_audit
    ADD CONSTRAINT lark_inbound_audit_installation_id_fkey FOREIGN KEY (installation_id) REFERENCES lark_installation(id) ON DELETE SET NULL;

ALTER TABLE ONLY lark_inbound_message_dedup
    ADD CONSTRAINT lark_inbound_message_dedup_installation_id_fkey FOREIGN KEY (installation_id) REFERENCES lark_installation(id) ON DELETE CASCADE;

ALTER TABLE ONLY lark_installation
    ADD CONSTRAINT lark_installation_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agent(id) ON DELETE CASCADE;

ALTER TABLE ONLY lark_installation
    ADD CONSTRAINT lark_installation_installer_user_id_fkey FOREIGN KEY (installer_user_id) REFERENCES "user"(id) ON DELETE RESTRICT;

ALTER TABLE ONLY lark_installation
    ADD CONSTRAINT lark_installation_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;

ALTER TABLE ONLY lark_outbound_card_message
    ADD CONSTRAINT lark_outbound_card_message_chat_session_id_fkey FOREIGN KEY (chat_session_id) REFERENCES chat_session(id) ON DELETE CASCADE;

ALTER TABLE ONLY lark_outbound_card_message
    ADD CONSTRAINT lark_outbound_card_message_task_id_fkey FOREIGN KEY (task_id) REFERENCES agent_task_queue(id) ON DELETE SET NULL;

ALTER TABLE ONLY lark_user_binding
    ADD CONSTRAINT lark_user_binding_installation_fk FOREIGN KEY (installation_id, workspace_id) REFERENCES lark_installation(id, workspace_id) ON DELETE CASCADE;

ALTER TABLE ONLY lark_user_binding
    ADD CONSTRAINT lark_user_binding_member_fk FOREIGN KEY (workspace_id, multica_user_id) REFERENCES member(workspace_id, user_id) ON DELETE CASCADE;
