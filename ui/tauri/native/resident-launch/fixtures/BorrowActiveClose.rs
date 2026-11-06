use resident_preparation::{NativePreparation, Preparation};
use std::io::Read;
fn main() {
    if let Preparation::Owned(mut owner) = NativePreparation::prepare() {
        let mut io = owner.io();
        owner.request_active_close();
        let _ = io.read(&mut [0]);
    }
}
