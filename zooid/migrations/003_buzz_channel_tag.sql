-- Buzz NIP-MP project discovery filters by the multi-character
-- `buzz-channel` tag. It is the sole application-specific extension to the
-- relay's otherwise single-letter tag index.
--
-- Existing events already retain their complete tags as JSON text. Backfill
-- one event_tags row for every historical buzz-channel value, preserving the
-- event kind used by the covering index. NOT EXISTS keeps this migration
-- idempotent and avoids duplicating rows saved by newer binaries before the
-- migration marker is written.
INSERT INTO {{.Name}}__event_tags (event_id, key, value, kind)
SELECT events.id, 'buzz-channel', tag.value ->> 1, events.kind
FROM {{.Name}}__events AS events
CROSS JOIN LATERAL jsonb_array_elements(events.tags::jsonb) AS tag(value)
WHERE jsonb_typeof(tag.value) = 'array'
  AND jsonb_array_length(tag.value) >= 2
  AND tag.value ->> 0 = 'buzz-channel'
  AND NOT EXISTS (
    SELECT 1
    FROM {{.Name}}__event_tags AS indexed
    WHERE indexed.event_id = events.id
      AND indexed.key = 'buzz-channel'
      AND indexed.value = tag.value ->> 1
  );
