package hostexecutor

const OwnedRootHome = "/workspace/home"

// These nonzero codes are an authenticated result on CRI's process-status
// channel, not bytes on a pipe. Generic exit0/1/2, exec126/127 and signal128+
// are never evidence. Keep all original peer/pidfd/RPC checks inside the probe.
const (
	EvidencePrepared = 40 + iota
	EvidenceStarting
	EvidenceReady
	EvidenceStopped
	EvidenceContinuable
)

func ValidEvidenceExit(code int) bool { return code >= EvidencePrepared && code <= EvidenceContinuable }

// EvidenceExit follows ProbeEvidence, which authenticates the original kernel
// PID1/native peer and fresh challenge. Recheck the operator's exact predicate
// here so no identity or phase field needs to traverse an untrusted output FD.
func EvidenceExit(p Observation, b Binding, homeUID, baseline string) int {
	if p.Version != 1 || !sameBinding(p.Binding, b) || homeUID == "" || p.HomeUID != homeUID || len(baseline) != 64 || p.Baseline != baseline {
		return 1
	}
	switch p.Stage {
	case "Prepared":
		if p.Root.PID == 0 && !p.RootWaitObserved && !p.TreeReaped {
			return EvidencePrepared
		}
	case "Starting":
		if !p.RootWaitObserved && !p.TreeReaped {
			return EvidenceStarting
		}
	case "Ready":
		if p.Root.PID > 1 && p.NativeVersion == PinnedNativeVersion && !p.RootWaitObserved && !p.TreeReaped {
			return EvidenceReady
		}
	case "Stopped":
		if p.Root.PID > 1 && p.RootWaitObserved && p.TreeReaped {
			if p.ContinuationReady {
				return EvidenceContinuable
			}
			return EvidenceStopped
		}
	}
	return 1
}
