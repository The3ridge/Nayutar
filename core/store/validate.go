package store

import "fmt"

// ValidateGet rejects Get arguments the contract does not define.
func ValidateGet(key string, atRev Rev) error {
	if key == "" {
		return fmt.Errorf("%w: empty key", ErrInvalidArgument)
	}
	if atRev < 0 {
		return fmt.Errorf("%w: negative revision %d", ErrInvalidArgument, atRev)
	}
	return nil
}

// ValidateRange rejects Range arguments the contract does not define.
func ValidateRange(prefix string, atRev Rev, limit int, cont string) error {
	if atRev < 0 {
		return fmt.Errorf("%w: negative revision %d", ErrInvalidArgument, atRev)
	}
	if limit < 0 {
		return fmt.Errorf("%w: negative limit %d", ErrInvalidArgument, limit)
	}
	if cont != "" && (len(cont) < len(prefix) || cont[:len(prefix)] != prefix) {
		return fmt.Errorf("%w: continuation %q is outside prefix %q", ErrInvalidArgument, cont, prefix)
	}
	return nil
}

// ValidateTxn rejects Txn arguments the contract does not define.
func ValidateTxn(cmps []Cmp, ops []Op) error {
	for i, c := range cmps {
		if c.Key == "" {
			return fmt.Errorf("%w: cmps[%d] has an empty key", ErrInvalidArgument, i)
		}
		if c.Op < CmpEqual || c.Op > CmpGreater {
			return fmt.Errorf("%w: cmps[%d] has unknown operator %d", ErrInvalidArgument, i, c.Op)
		}
		if c.ModRev < 0 {
			return fmt.Errorf("%w: cmps[%d] has negative revision %d", ErrInvalidArgument, i, c.ModRev)
		}
	}
	seen := make(map[string]struct{}, len(ops))
	for i, op := range ops {
		if op.Key == "" {
			return fmt.Errorf("%w: ops[%d] has an empty key", ErrInvalidArgument, i)
		}
		if op.Type != OpPut && op.Type != OpDelete {
			return fmt.Errorf("%w: ops[%d] has unknown type %d", ErrInvalidArgument, i, op.Type)
		}
		if _, dup := seen[op.Key]; dup {
			return fmt.Errorf("%w: ops[%d] repeats key %q", ErrInvalidArgument, i, op.Key)
		}
		seen[op.Key] = struct{}{}
	}
	return nil
}

// ValidateWatch rejects Watch arguments the contract does not define.
func ValidateWatch(fromRev Rev) error {
	if fromRev < 0 {
		return fmt.Errorf("%w: negative revision %d", ErrInvalidArgument, fromRev)
	}
	return nil
}

// ValidateCompact rejects Compact arguments the contract does not define.
func ValidateCompact(rev Rev) error {
	if rev < 1 {
		return fmt.Errorf("%w: compaction revision %d is below 1", ErrInvalidArgument, rev)
	}
	return nil
}
