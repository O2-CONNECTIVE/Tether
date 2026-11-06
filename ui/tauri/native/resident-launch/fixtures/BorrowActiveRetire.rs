use resident_preparation::{NativePreparation, Preparation};
use std::io::Read;
fn main() {
    if let Preparation::Owned(mut owner) = NativePreparation::prepare() {
        let mut io = owner.io();
        let _ = owner.retire_active_io();
        let _ = io.read(&mut [0]);
    }
}
