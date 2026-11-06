package signatures

import "path/filepath"

// Normal publication never performs rollback, including main and bytecode.
// Versions are from verified CVDs, not download times. Caller holds publisher EX.
func (s Store) checkAdvance(active, candidate string, next Manifest) error {
	if active == "" {
		return nil
	}
	path, err := s.generation(active)
	if err != nil {
		return err
	}
	var previous Manifest
	if err = readJSON(filepath.Join(path, "manifest.json"), &previous); err != nil {
		return err
	}
	if previous.ID() != active {
		return ErrUntrusted
	}
	for i, name := range databaseNames {
		if err = advancingDatabase(filepath.Join(path, name), filepath.Join(candidate, name), previous.Hashes[i], next.Hashes[i]); err != nil {
			return err
		}
	}
	return nil
}

func advancingDatabase(previous, next, previousHash, nextHash string) error {
	actual, err := databaseHash(previous)
	if err != nil || actual != previousHash {
		return ErrUntrusted
	}
	actual, err = databaseHash(next)
	if err != nil || actual != nextHash {
		return ErrUntrusted
	}
	before, _, err := databaseHeader(previous)
	if err != nil {
		return err
	}
	after, _, err := databaseHeader(next)
	if err != nil {
		return err
	}
	if after < before || (after == before && previousHash != nextHash) {
		return ErrUntrusted
	}
	return nil
}
