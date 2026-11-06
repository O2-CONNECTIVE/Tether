// Compiler-overlay-only observations of the original lease; not authority.
package agenttransport

func ExportStandbyLeaseReading(s *ResidentSourceLease) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reading != nil
}
func ExportStandbyLeaseFinished(s *ResidentSourceLease) bool { return s.completed() }
