use resident_preparation::NativePreparation;
fn require_send<T: Send>() {}
fn main() {
    require_send::<NativePreparation>();
}
