package netpol

import "io/fs"

type DirFact struct {
	Path string
	Mode fs.FileMode
	UID  uint32
}

// NewExecNftRunnerTrustingUID trusts uid for the binary and skips the parent-directory check,
// because root-owned paths show up as an unmapped uid inside a user namespace.
func NewExecNftRunnerTrustingUID(binPath string, uid uint32, opts ...ExecOption) (*ExecNftRunner, error) {
	return newExecNftRunner(binPath, uid, true, opts)
}

func CheckNftBinFacts(path string, mode fs.FileMode, uid, trustedUID uint32, dirs []DirFact) error {
	conv := make([]dirFact, len(dirs))
	for i, d := range dirs {
		conv[i] = dirFact{path: d.Path, mode: d.Mode, uid: d.UID}
	}
	return checkNftBinFacts(path, mode, uid, trustedUID, conv)
}

const (
	GuardForwardChain = guardForwardChain
	GuardInputChain   = guardInputChain
)
