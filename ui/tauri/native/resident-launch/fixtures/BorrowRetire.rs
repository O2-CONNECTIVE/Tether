use resident_preparation::{NativePreparation, Preparation};
fn main() {
    let Preparation::Owned(mut owner) = NativePreparation::prepare() else {
        return;
    };
    let mut view = owner.io();
    let _ = owner.retire();
    let _ = view.half_close_control();
}
