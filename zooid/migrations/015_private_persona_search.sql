-- Persona content may include plaintext prompts and must never be searchable.
-- The startup trigger definition keeps future writes out. This migration
-- clears vectors produced by relay versions that indexed kind 30175.
UPDATE {{.Name}}__events
SET search_vector = NULL
WHERE kind = 30175
  AND search_vector IS NOT NULL;
