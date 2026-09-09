ALTER TABLE participants ADD COLUMN lid TEXT;
ALTER TABLE participants ADD COLUMN phone_address TEXT;
ALTER TABLE sender_refs ADD COLUMN lid TEXT;
ALTER TABLE inbound_events ADD COLUMN sender_lid TEXT;

UPDATE participants SET lid = provider_address
WHERE provider_address LIKE '%@lid' OR provider_address LIKE '%@hosted.lid';
UPDATE participants SET phone_address = provider_address
WHERE provider_address LIKE '%@s.whatsapp.net' OR provider_address LIKE '%@c.us';
UPDATE sender_refs SET lid = (
    SELECT participants.lid FROM participants
    WHERE participants.tenant_id = sender_refs.tenant_id
      AND participants.account_id = sender_refs.account_id
      AND participants.id = sender_refs.participant_id
) WHERE lid IS NULL;
UPDATE inbound_events SET sender_lid = (
    SELECT participants.lid FROM participants
    WHERE participants.tenant_id = inbound_events.tenant_id
      AND participants.account_id = inbound_events.account_id
      AND participants.id = inbound_events.participant_id
) WHERE sender_lid IS NULL;

CREATE UNIQUE INDEX participants_lid_idx
ON participants(tenant_id, account_id, lid) WHERE lid IS NOT NULL;
CREATE UNIQUE INDEX participants_phone_idx
ON participants(tenant_id, account_id, phone_address) WHERE phone_address IS NOT NULL;
CREATE UNIQUE INDEX sender_refs_lid_idx
ON sender_refs(tenant_id, account_id, chat_id, lid) WHERE lid IS NOT NULL;
