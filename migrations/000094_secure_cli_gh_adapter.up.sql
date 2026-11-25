-- Route existing `gh` secure CLI binaries through the new `gh` credential
-- adapter so agents can use typed credentials (pat / github_app) for gh.
--
-- Safe for existing rows: the gh adapter is a no-op for legacy env
-- credentials (GH_TOKEN env keeps being injected exactly as before), and it is
-- sandbox-compatible. Only rows that have no adapter yet are touched, so an
-- operator-chosen adapter_name is never overwritten.
UPDATE secure_cli_binaries
   SET adapter_name = 'gh',
       updated_at = NOW()
 WHERE binary_name = 'gh'
   AND (adapter_name IS NULL OR adapter_name = '');
