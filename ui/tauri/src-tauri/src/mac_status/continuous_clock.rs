//! Same-boot, sleep-inclusive time for the Mac status source.
//! This module does not open a pipe, install a provider or authorize a connection.

const MAX_SAFE_MILLIS: u128 = 9_007_199_254_740_991;

fn continuous_millis(ticks: u64, numerator: u32, denominator: u32) -> Option<u64> {
    if numerator == 0 || denominator == 0 {
        return None;
    }
    // Match the existing Go producer: multiply before dividing, using u128.
    let millis = u128::from(ticks) * u128::from(numerator) / u128::from(denominator) / 1_000_000;
    (millis <= MAX_SAFE_MILLIS).then_some(millis as u64)
}

#[cfg(target_os = "macos")]
mod native {
    use super::continuous_millis;

    #[repr(C)]
    #[derive(Default)]
    struct Timebase {
        numer: u32,
        denom: u32,
    }

    unsafe extern "C" {
        fn mach_timebase_info(info: *mut Timebase) -> i32;
        fn mach_continuous_time() -> u64;
    }

    pub struct ContinuousClock {
        timebase: Timebase,
    }

    impl ContinuousClock {
        pub fn new() -> Option<Self> {
            let mut timebase = Timebase::default();
            // SAFETY: a valid writable C-layout structure lives for this call.
            let result = unsafe { mach_timebase_info(&mut timebase) };
            if result != 0 || timebase.numer == 0 || timebase.denom == 0 {
                return None;
            }
            Some(Self { timebase })
        }

        pub fn now_seconds(&self) -> Option<f64> {
            self.now_millis().map(|millis| millis as f64 / 1000.0)
        }

        pub fn now_millis(&self) -> Option<u64> {
            // SAFETY: this argument-free system call has no ownership contract.
            let ticks = unsafe { mach_continuous_time() };
            continuous_millis(ticks, self.timebase.numer, self.timebase.denom)
        }
    }
}

#[cfg(target_os = "macos")]
pub use native::ContinuousClock;

#[cfg(test)]
mod tests {
    use super::continuous_millis;

    #[test]
    fn invalid_timebases_fail_closed() {
        assert_eq!(continuous_millis(1, 0, 1), None);
        assert_eq!(continuous_millis(1, 1, 0), None);
    }

    #[test]
    fn boot_zero_is_valid() {
        assert_eq!(continuous_millis(0, 125, 3), Some(0));
    }

    #[test]
    fn fractional_milliseconds_floor_like_the_producer() {
        assert_eq!(continuous_millis(23_999, 125, 3), Some(0));
        assert_eq!(continuous_millis(24_000, 125, 3), Some(1));
        assert_eq!(continuous_millis(47_999, 125, 3), Some(1));
        assert_eq!(continuous_millis(2, 1_500_000, 3), Some(1));
    }

    #[test]
    fn multiplication_does_not_wrap_u64() {
        let expected = (u128::from(u64::MAX) * 2 / 1_000_000) as u64;
        assert_eq!(continuous_millis(u64::MAX, 2, 1), Some(expected));
    }

    #[test]
    fn exact_wire_safe_boundary_is_accepted() {
        assert_eq!(
            continuous_millis(9_007_199_254_740_991, 1_000_000, 1),
            Some(9_007_199_254_740_991)
        );
        assert_eq!(continuous_millis(9_007_199_254_740_992, 1_000_000, 1), None);
    }

    #[test]
    fn large_timebase_overflow_of_wire_range_is_rejected() {
        assert_eq!(continuous_millis(u64::MAX, u32::MAX, 1), None);
    }

    #[test]
    fn elapsed_ticks_are_not_reset_or_rebased() {
        let before = continuous_millis(24_000_000, 125, 3).unwrap();
        let after = continuous_millis(264_000_000, 125, 3).unwrap();
        assert_eq!(after - before, 10_000);
    }
}
