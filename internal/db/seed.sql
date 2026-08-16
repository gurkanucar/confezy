-- Demo data, inserted once into an empty database so the admin UI has
-- something to show. It deliberately covers the awkward cases: minimum and
-- maximum length keys, empty descriptions, every JSON shape, text that must be
-- HTML-escaped, and both active and revoked API keys.
--
-- Rows use explicit ids because seeding only runs when the database holds no
-- projects, so the ids are free and referencing them stays readable.
--
-- The API keys below are display-only: the stored hashes correspond to no real
-- key, so nothing here grants access. Create a usable key from the UI.

-- Projects ------------------------------------------------------------------

INSERT INTO projects (id, slug, name, created_at) VALUES
  (1, 'checkout',       'Checkout Service', CAST(strftime('%s','now') AS INTEGER) - 86400 * 30),
  (2, 'mobile-app',     'Mobile App',       CAST(strftime('%s','now') AS INTEGER) - 86400 * 14),
  (3, 'internal_tools', 'Internal Tools',   CAST(strftime('%s','now') AS INTEGER) - 86400 * 2);

-- Environments --------------------------------------------------------------
-- checkout has the full ladder, mobile-app has two, internal_tools only prod.

INSERT INTO environments (id, project_id, slug, updated_at, created_at) VALUES
  (1, 1, 'prod',    CAST(strftime('%s','now') AS INTEGER) - 3600,       CAST(strftime('%s','now') AS INTEGER) - 86400 * 30),
  (2, 1, 'staging', CAST(strftime('%s','now') AS INTEGER) - 86400,      CAST(strftime('%s','now') AS INTEGER) - 86400 * 30),
  (3, 1, 'dev',     CAST(strftime('%s','now') AS INTEGER) - 86400 * 2,  CAST(strftime('%s','now') AS INTEGER) - 86400 * 29),
  (4, 2, 'prod',    CAST(strftime('%s','now') AS INTEGER) - 7200,       CAST(strftime('%s','now') AS INTEGER) - 86400 * 14),
  (5, 2, 'beta',    CAST(strftime('%s','now') AS INTEGER) - 1800,       CAST(strftime('%s','now') AS INTEGER) - 86400 * 14),
  (6, 3, 'prod',    CAST(strftime('%s','now') AS INTEGER) - 86400 * 2,  CAST(strftime('%s','now') AS INTEGER) - 86400 * 2);

-- Feature flags -------------------------------------------------------------
-- checkout/prod is the showcase environment.

INSERT INTO feature_flags (environment_id, key, enabled, description, version, updated_at) VALUES
  -- The ordinary cases.
  (1, 'new_checkout', 1, 'Routes traffic to the rewritten checkout flow.', 7,
      CAST(strftime('%s','now') AS INTEGER) - 3600),
  (1, 'show_ads', 0, '', 1,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 12),

  -- Shortest legal key (one character) and the 64-character maximum.
  (1, 'a', 1, 'Single character key, the shortest the format allows.', 2,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 5),
  (1, 'a_very_long_flag_key_that_reaches_the_sixty_four_character_limit', 0,
      'Longest key the format allows, for checking table layout.', 1,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 5),

  -- Long description, to see how the column wraps.
  (1, 'gradual_rollout', 1,
      'Enables the staged rollout scheduler. When this is on, the service reads the cohort ' ||
      'definitions from the rollout_cohorts config and assigns every incoming request to a ' ||
      'bucket, which is why turning it off also stops cohort reporting.', 23,
      CAST(strftime('%s','now') AS INTEGER) - 900),

  -- Text that has to survive HTML escaping.
  (1, 'escaping_check', 0,
      'Quotes "double" & ''single'', a tag <script>alert(1)</script>, an ampersand & a backslash \',
      3, CAST(strftime('%s','now') AS INTEGER) - 86400),

  -- Non-ASCII description.
  (1, 'unicode_check', 1, 'Ünïcödé: ĞŞİÖÇüğı, 日本語, Ελληνικά, 🎉 emoji', 4,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 3),

  -- Digits and underscores, high version number.
  (1, 'api_v2_enabled', 1, 'Serves the v2 API surface.', 142,
      CAST(strftime('%s','now') AS INTEGER) - 120),

  -- The same keys in other environments, deliberately out of sync with prod.
  (2, 'new_checkout', 1, 'Routes traffic to the rewritten checkout flow.', 12,
      CAST(strftime('%s','now') AS INTEGER) - 86400),
  (2, 'show_ads', 1, 'Enabled in staging so QA can see the ad slots.', 4,
      CAST(strftime('%s','now') AS INTEGER) - 86400),
  (2, 'debug_toolbar', 1, 'Shows the in-page debug toolbar.', 1,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 4),

  (3, 'new_checkout', 1, '', 2, CAST(strftime('%s','now') AS INTEGER) - 86400 * 2),
  (3, 'debug_toolbar', 1, 'Always on in dev.', 1, CAST(strftime('%s','now') AS INTEGER) - 86400 * 2),

  (4, 'dark_mode', 1, 'Ships the dark theme to everyone.', 9,
      CAST(strftime('%s','now') AS INTEGER) - 7200),
  (4, 'offline_sync', 0, 'Background sync while the app has no connection.', 15,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 6),

  (5, 'dark_mode', 1, 'Ships the dark theme to everyone.', 11,
      CAST(strftime('%s','now') AS INTEGER) - 1800),
  (5, 'offline_sync', 1, 'Under test with the beta group.', 18,
      CAST(strftime('%s','now') AS INTEGER) - 1800),
  (5, 'crash_reporter_verbose', 0, 'Verbose crash payloads. Costs bandwidth.', 2,
      CAST(strftime('%s','now') AS INTEGER) - 86400);

-- internal_tools/prod is left without flags on purpose, so the empty state is
-- visible somewhere.

-- JSON configs --------------------------------------------------------------
-- Every JSON shape the storage accepts: objects, arrays, deep nesting, all four
-- scalar types, empty containers, and text that needs escaping.

INSERT INTO configs (environment_id, key, value, description, version, updated_at) VALUES
  (1, 'payment_rules',
      '{"minimumAmount":50,"maximumAmount":5000,"currency":"TRY","allowedMethods":["card","transfer","wallet"]}',
      'Limits applied before a payment is accepted.', 5,
      CAST(strftime('%s','now') AS INTEGER) - 3600),

  -- Array at the top level, holding objects.
  (1, 'rollout_cohorts',
      '[{"name":"internal","percentage":100,"since":"2026-01-04"},' ||
      '{"name":"early_access","percentage":25,"since":"2026-02-11"},' ||
      '{"name":"everyone","percentage":5,"since":null}]',
      'Cohorts used by the gradual_rollout flag.', 12,
      CAST(strftime('%s','now') AS INTEGER) - 900),

  -- Deep nesting.
  (1, 'deep_nesting',
      '{"level1":{"level2":{"level3":{"level4":{"level5":{"level6":{"value":"bottom","reached":true}}}}}}}',
      'Six levels deep, to check the editor stays readable.', 1,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 8),

  -- Every scalar type in one document, plus null and an empty container.
  (1, 'mixed_types',
      '{"string":"text","integer":42,"negative":-17,"float":3.14159,"exponent":1.2e10,' ||
      '"boolTrue":true,"boolFalse":false,"nullValue":null,"emptyObject":{},"emptyArray":[],' ||
      '"arrayOfMixed":[1,"two",false,null,{"three":3},[4]]}',
      'One of every JSON type the format allows.', 3,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 2),

  -- Text that must survive both JSON and HTML escaping.
  (1, 'escaping_check',
      '{"quotes":"He said \"hello\" loudly","apostrophe":"it''s fine",' ||
      '"backslash":"C:\\temp\\new","newline":"first line\nsecond line","tab":"a\tb",' ||
      '"closingTag":"</script><b>bold</b>","ampersand":"a & b","unicode":"Ünïcödé ĞŞİÖÇ 日本語 🎉"}',
      'Quotes, backslashes, tags and non-ASCII text.', 2,
      CAST(strftime('%s','now') AS INTEGER) - 86400),

  -- Empty containers on their own.
  (1, 'empty_object', '{}', 'An empty object is valid JSON.', 1,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 9),
  (1, 'empty_array', '[]', 'An empty array is valid JSON.', 1,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 9),

  -- Bare scalars: the format allows these as whole documents.
  (1, 'scalar_number', '42', 'A bare number is a valid document.', 1,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 9),
  (1, 'scalar_string', '"just a string"', 'A bare string is a valid document.', 1,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 9),
  (1, 'scalar_bool', 'true', 'A bare boolean is a valid document.', 1,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 9),
  (1, 'scalar_null', 'null', 'A bare null is a valid document.', 1,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 9),

  -- Larger document, so one card is clearly taller than the others.
  (1, 'feature_matrix',
      '{"regions":{"tr":{"enabled":true,"currency":"TRY","vat":0.2,"methods":["card","transfer"]},' ||
      '"de":{"enabled":true,"currency":"EUR","vat":0.19,"methods":["card","sepa"]},' ||
      '"uk":{"enabled":false,"currency":"GBP","vat":0.2,"methods":["card"]},' ||
      '"us":{"enabled":true,"currency":"USD","vat":0.0,"methods":["card","wallet"]}},' ||
      '"limits":{"perTransaction":10000,"perDay":50000,"perMonth":250000},' ||
      '"retries":{"count":3,"backoffMs":[200,800,3200],"giveUpAfterMs":15000},' ||
      '"notifications":{"email":true,"sms":false,"push":true,"webhook":null}}',
      'Region matrix, limits and retry policy in one document.', 31,
      CAST(strftime('%s','now') AS INTEGER) - 300),

  -- Key at the 64-character maximum.
  (1, 'a_very_long_config_key_that_reaches_the_sixty_four_char_maximum',
      '{"note":"the key is at the maximum allowed length"}',
      'Longest key the format allows.', 1,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 9),

  -- Empty description, to see the field render blank.
  (1, 'no_description', '{"documented":false}', '', 1,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 9),

  -- Other environments.
  (2, 'payment_rules',
      '{"minimumAmount":1,"maximumAmount":999999,"currency":"TRY","allowedMethods":["card"]}',
      'Loose limits so QA can test edge amounts.', 8,
      CAST(strftime('%s','now') AS INTEGER) - 86400),
  (2, 'test_accounts',
      '[{"email":"qa1@example.com","balance":1000},{"email":"qa2@example.com","balance":0}]',
      'Accounts reset nightly.', 4,
      CAST(strftime('%s','now') AS INTEGER) - 86400),

  (3, 'payment_rules',
      '{"minimumAmount":0,"maximumAmount":100,"currency":"TRY","allowedMethods":["card","transfer","wallet"]}',
      '', 2, CAST(strftime('%s','now') AS INTEGER) - 86400 * 2),

  (4, 'api_endpoints',
      '{"base":"https://api.example.com","timeoutMs":8000,"retries":2,' ||
      '"paths":{"login":"/v2/auth/login","profile":"/v2/me","feed":"/v2/feed"}}',
      'Endpoints the mobile client talks to.', 6,
      CAST(strftime('%s','now') AS INTEGER) - 7200),
  (4, 'force_update',
      '{"minimumVersion":"4.2.0","blocking":false,"message":"A new version is available."}',
      'Drives the update prompt on launch.', 3,
      CAST(strftime('%s','now') AS INTEGER) - 86400 * 3),

  (5, 'api_endpoints',
      '{"base":"https://api-beta.example.com","timeoutMs":15000,"retries":0,' ||
      '"paths":{"login":"/v2/auth/login","profile":"/v2/me","feed":"/v2/feed/experimental"}}',
      'Beta build points at the beta cluster.', 9,
      CAST(strftime('%s','now') AS INTEGER) - 1800);

-- API keys ------------------------------------------------------------------
-- Display only: these hashes match no real key, so none of them authenticate.
-- They exist so the table shows every scope plus the revoked state. Create a
-- working key from the UI.

INSERT INTO api_keys (environment_id, key_hash, key_prefix, scope, label, created_at, revoked_at) VALUES
  (1, 'demo0000000000000000000000000000000000000000000000000000000001', 'ff_read_prod', 'read',  'ios-app',      CAST(strftime('%s','now') AS INTEGER) - 86400 * 20, NULL),
  (1, 'demo0000000000000000000000000000000000000000000000000000000002', 'ff_read_prod', 'read',  'android-app',  CAST(strftime('%s','now') AS INTEGER) - 86400 * 18, NULL),
  (1, 'demo0000000000000000000000000000000000000000000000000000000003', 'ff_writ_prod', 'write', 'ci-pipeline',  CAST(strftime('%s','now') AS INTEGER) - 86400 * 15, NULL),
  -- Empty label, to see the column render blank.
  (1, 'demo0000000000000000000000000000000000000000000000000000000004', 'ff_admi_prod', 'admin', '',             CAST(strftime('%s','now') AS INTEGER) - 86400 * 10, NULL),
  -- Revoked, so the inactive styling is visible.
  (1, 'demo0000000000000000000000000000000000000000000000000000000005', 'ff_read_prod', 'read',  'retired-web',  CAST(strftime('%s','now') AS INTEGER) - 86400 * 40, CAST(strftime('%s','now') AS INTEGER) - 86400 * 6),
  (2, 'demo0000000000000000000000000000000000000000000000000000000006', 'ff_read_stag', 'read',  'qa-runner',    CAST(strftime('%s','now') AS INTEGER) - 86400 * 12, NULL),
  (4, 'demo0000000000000000000000000000000000000000000000000000000007', 'ff_read_prod', 'read',  'mobile-prod',  CAST(strftime('%s','now') AS INTEGER) - 86400 * 9,  NULL);

-- Tags ----------------------------------------------------------------------
-- Defined per project, then attached to flags and configs across environments.

INSERT INTO tags (id, project_id, name, created_at) VALUES
  (1, 1, 'checkout',    CAST(strftime('%s','now') AS INTEGER) - 86400 * 25),
  (2, 1, 'experiment',  CAST(strftime('%s','now') AS INTEGER) - 86400 * 25),
  (3, 1, 'risky',       CAST(strftime('%s','now') AS INTEGER) - 86400 * 20),
  (4, 1, 'billing',     CAST(strftime('%s','now') AS INTEGER) - 86400 * 20),
  (5, 1, 'internal',    CAST(strftime('%s','now') AS INTEGER) - 86400 * 18),
  (6, 2, 'ui',          CAST(strftime('%s','now') AS INTEGER) - 86400 * 12),
  (7, 2, 'performance', CAST(strftime('%s','now') AS INTEGER) - 86400 * 12);

INSERT INTO flag_tags (flag_id, tag_id)
SELECT f.id, t.id FROM feature_flags f, tags t
 WHERE (f.environment_id = 1 AND f.key = 'new_checkout'    AND t.id IN (1, 2))
    OR (f.environment_id = 1 AND f.key = 'gradual_rollout' AND t.id IN (2, 3))
    OR (f.environment_id = 1 AND f.key = 'api_v2_enabled'  AND t.id IN (3))
    OR (f.environment_id = 1 AND f.key = 'show_ads'        AND t.id IN (2))
    OR (f.environment_id = 1 AND f.key = 'escaping_check'  AND t.id IN (5))
    OR (f.environment_id = 1 AND f.key = 'unicode_check'   AND t.id IN (5))
    OR (f.environment_id = 2 AND f.key = 'new_checkout'    AND t.id IN (1))
    OR (f.environment_id = 2 AND f.key = 'debug_toolbar'   AND t.id IN (5))
    OR (f.environment_id = 4 AND f.key = 'dark_mode'       AND t.id IN (6))
    OR (f.environment_id = 5 AND f.key = 'dark_mode'       AND t.id IN (6))
    OR (f.environment_id = 5 AND f.key = 'offline_sync'    AND t.id IN (6, 7));

INSERT INTO config_tags (config_id, tag_id)
SELECT c.id, t.id FROM configs c, tags t
 WHERE (c.environment_id = 1 AND c.key = 'payment_rules'   AND t.id IN (1, 4))
    OR (c.environment_id = 1 AND c.key = 'rollout_cohorts' AND t.id IN (2))
    OR (c.environment_id = 1 AND c.key = 'feature_matrix'  AND t.id IN (4))
    OR (c.environment_id = 1 AND c.key = 'escaping_check'  AND t.id IN (5))
    OR (c.environment_id = 1 AND c.key = 'mixed_types'     AND t.id IN (5))
    OR (c.environment_id = 2 AND c.key = 'payment_rules'   AND t.id IN (1, 4))
    OR (c.environment_id = 4 AND c.key = 'api_endpoints'   AND t.id IN (7));
