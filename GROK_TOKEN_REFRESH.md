# Grok OAuth Token Refresh Capacity

The token refresh scanner is configured for Grok pools up to 60,000 accounts by
default. The high-capacity settings are scoped to Grok; other OAuth providers
retain the conservative global concurrency and QPS limits.

## Default Capacity Profile

```yaml
token_refresh:
  enabled: true
  check_interval_minutes: 5
  refresh_before_expiry_hours: 0.5
  candidate_page_size: 1000
  provider_concurrency: 4
  provider_qps: 2
  grok_provider_concurrency: 32
  grok_provider_qps: 25
  grok_refresh_jitter_minutes: 60
  provider_failure_threshold: 3
  max_retries: 3
  retry_backoff_seconds: 2
  attempt_timeout_seconds: 15
  cycle_timeout_seconds: 3600
```

Grok always refreshes at least one hour before expiry. The deterministic jitter
adds a stable 0-60 minute offset per account, spreading synchronized imports
over time without changing after a restart or leader handoff.

At 25 QPS, a completely synchronized 60,000-account pool needs about 40 minutes
for one attempt per account. Retries consume the same QPS budget.

## Multi-instance Behavior

Every server process starts the refresh service, but only one process acquires
the distributed cycle leader lock. Redis is preferred, with a PostgreSQL
advisory-lock fallback. Non-leaders skip the cycle before querying candidate
accounts. The lock TTL is the configured cycle timeout plus one minute and is
released with owner-token comparison when the cycle completes.

Account-level refresh locks remain in place. They still coordinate background
refresh with request-path refresh for the same account.

## Monitoring

Track these structured log events:

- `token_refresh.cycle_completed`: candidate, refresh, skip, and failure counts.
- `token_refresh.cycle_stopped`: the cycle reached its timeout; inspect `resume_after_id`.
- `token_refresh.account_refresh_failed`: upstream or persistence failure.
- `token_refresh.cycle_skipped_not_leader`: expected on non-leader instances.

Reduce `grok_provider_qps` if upstream `429` responses or provider-cycle
containment increase. Increase it only after confirming refresh latency and
upstream acceptance; the hard maximum remains 100 QPS.
