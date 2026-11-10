# Allowance reading fixtures

`claude-statusline-helper.json` is the document the image's
`/usr/local/bin/claude-statusline-usage --output FILE` wrote on 2026-11-10,
in a session pod on image 2.97.0, from synthetic callback input:

```json
{"session_id":"synthetic","version":"2.1.321","model":{"id":"claude-opus-5-5"},
 "rate_limits":{"five_hour":{"used_percentage":12.5,"resets_at":<now+2h>},
                "seven_day":{"used_percentage":34,"resets_at":<now+3d>}}}
```

No provider was contacted and no value comes from a real account. It shows
the exact format agentd reads at each heartbeat (DESIGN-059 step 3).
