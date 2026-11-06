use resident_preparation::{NativePreparation, Preparation};
use std::io::Read;
fn main() {
    if let Preparation::Owned(mut owner) = NativePreparation::prepare() {
        let mut io = owner.io();
        let _ = owner.poll_exit();
        let _ = io.read(&mut [0]);
    }
}
