# Rate-limit /api/login

Failed logins should be throttled per account.

## Acceptance criteria
- After 5 failed logins for one account within 15 minutes, further attempts get HTTP 429.
- A successful login clears that account's failure count.
