use resident_preparation::NativePreparation;
fn require_sync<T: Sync>() {}
fn main() {
    require_sync::<NativePreparation>();
}
