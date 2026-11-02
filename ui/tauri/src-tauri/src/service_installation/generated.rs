// Ordinary builds have no installation seed. Only the paired offline producer
// may generate a host-specific evaluation replacement; never hand-edit values.
use super::Seed;
pub(super) fn seed() -> Option<Seed> { None }
