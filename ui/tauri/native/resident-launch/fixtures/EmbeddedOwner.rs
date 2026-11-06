// Mirrors the existing native source consumer: prepared IO is bound, active
// observations remain unused until the later Pump/supervisor connection.
#[allow(
    dead_code,
    reason = "same unbound source embedding as the existing consumer"
)]
#[path = "../rust/owner.rs"]
mod native_preparation;
fn require_duplex<T: std::io::Read + std::io::Write>() {}
fn main() {
    use native_preparation::{NativePreparation, PreparedIo};
    require_duplex::<PreparedIo<'static>>();
    let _ = NativePreparation::prepare();
    #[cfg(resident_launch_fixture)]
    {
        use native_preparation::{Counts, FixedFixture};
        let _ = NativePreparation::fixture(FixedFixture::Normal);
        let _ = Option::<Counts>::None;
    }
}
