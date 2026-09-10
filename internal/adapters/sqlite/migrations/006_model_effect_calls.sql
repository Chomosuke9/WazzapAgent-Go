ALTER TABLE typed_effects ADD COLUMN model_call_id TEXT;

CREATE UNIQUE INDEX typed_effects_model_call_idx
    ON typed_effects(tenant_id, account_id, chat_id, invocation_id, model_call_id)
    WHERE model_call_id IS NOT NULL;
