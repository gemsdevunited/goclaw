-- Revert gh rows to the legacy passthrough adapter. Typed gh credentials
-- (pat / github_app) stop authenticating until the adapter is re-enabled;
-- legacy GH_TOKEN env credentials keep working.
UPDATE secure_cli_binaries
   SET adapter_name = NULL,
       updated_at = NOW()
 WHERE binary_name = 'gh'
   AND adapter_name = 'gh';
