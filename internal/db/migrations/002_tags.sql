-- Tags are defined per project, not per environment, so the same label does not
-- have to be recreated in prod, staging and dev. The join tables hang off the
-- flag/config rows, which are per environment.

CREATE TABLE tags (
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE (project_id, name)
);

CREATE TABLE flag_tags (
  flag_id INTEGER NOT NULL REFERENCES feature_flags(id) ON DELETE CASCADE,
  tag_id  INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  PRIMARY KEY (flag_id, tag_id)
);

CREATE INDEX idx_flag_tags_tag_id ON flag_tags(tag_id);

CREATE TABLE config_tags (
  config_id INTEGER NOT NULL REFERENCES configs(id) ON DELETE CASCADE,
  tag_id    INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  PRIMARY KEY (config_id, tag_id)
);

CREATE INDEX idx_config_tags_tag_id ON config_tags(tag_id);
