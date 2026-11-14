package summonoutcome

import "github.com/thaynes43/dev-env/api/v1alpha1"

// Words shared by filing, abandonment and its future pager (DESIGN-064 6.6,
// OP801; coordinator rulings on item 768 comment 7702 and item 801 comment
// 7852). This file holds no ledger code: summonledger imports this package.
const (
	LabelFiledForUID                                       = v1alpha1.LabelPrefix + "filed-for-uid"
	ConditionEscalationPending                             = "EscalationPending"
	FilingPolicyGone           v1alpha1.SummonFilingReason = "policy-gone"
	FilingPolicyNoEscalations  v1alpha1.SummonFilingReason = "policy-no-escalations"
	FilingHourFull             v1alpha1.SummonFilingReason = "hour-full"
	FilingQueueFull            v1alpha1.SummonFilingReason = "queue-full"
	FilingRemoteOff            v1alpha1.SummonFilingReason = "remote-off"
	FilingLedgerUnavailable    v1alpha1.SummonFilingReason = "ledger-unavailable"
	FilingNoFreeName           v1alpha1.SummonFilingReason = "no-free-name"
	FilingEntryFull            v1alpha1.SummonFilingReason = "entry-full"
	FilingNotFiled             v1alpha1.SummonFilingReason = "not-filed"
)
