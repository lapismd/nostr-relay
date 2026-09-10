-- Encrypted agent payloads must never remain in the full-text index. The
-- startup trigger definition keeps future writes out. This migration removes
-- vectors produced by older relay versions.
UPDATE {{.Name}}__events
SET search_vector = NULL
WHERE kind IN (30174, 30179, 44100, 44101, 44200)
  AND search_vector IS NOT NULL;
