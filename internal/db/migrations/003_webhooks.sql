-- Webhooks fire when anything inside their environment changes. They are the
-- push counterpart to the ETag polling loop: a receiver that gets one of these
-- knows to re-fetch the snapshot now instead of waiting for its next poll.
--
-- The request carries no body on purpose. What changed is not described here;
-- the receiver asks the API. That keeps the delivery a pure signal and avoids
-- shipping config values to whatever the URL points at.

CREATE TABLE webhooks (
  id             INTEGER PRIMARY KEY,
  environment_id INTEGER NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  url            TEXT NOT NULL,
  method         TEXT NOT NULL DEFAULT 'PATCH',
  headers        TEXT NOT NULL DEFAULT '{}',   -- JSON object: header name -> value
  label          TEXT NOT NULL DEFAULT '',
  enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  created_at     INTEGER NOT NULL,

  -- Result of the most recent attempt, so the panel can show whether the
  -- receiver is actually reachable. This is a status line, not a delivery log.
  last_status    INTEGER,
  last_error     TEXT NOT NULL DEFAULT '',
  last_fired_at  INTEGER
);

CREATE INDEX idx_webhooks_environment_id ON webhooks(environment_id);
