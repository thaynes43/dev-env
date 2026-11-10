package apiv1

// CallerPolicyLimitsHeader negotiates limits.maxWorkers on existing callers'
// session responses. The new policy caller always receives its own bounds;
// old strict readers keep the pre-CallerPolicy Limits shape unless they opt in.
const CallerPolicyLimitsHeader = "Dev-Env-Caller-Policy"
const CallerPolicyLimitsV1 = "limits-v1"
