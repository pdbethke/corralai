// Fixture for the "rust" langSpecs row, in three labelled sections. Sections A
// and B are REAL files (dump-first: the parser is pinned against code that
// exists, not code written to pass); section C is constructed, for the forms
// neither real file carries.
//
// ---- A: BurntSushi/byteorder src/io.rs @ 5a82625fae462e8ba64cec8146b24a372b4d75c6
// TRIMMED: doc comments dropped and read_u16..read_f64_into_unchecked cut,
// keeping the imports, the trait head, read_u8, read_i8, read_u16_into (an
// unsafe block) and the blanket `impl ... ReadBytesExt for R {}` unchanged.
use std::{
    io::{self, Result},
    slice,
};

use crate::ByteOrder;

pub trait ReadBytesExt: io::Read {
    #[inline]
    fn read_u8(&mut self) -> Result<u8> {
        let mut buf = [0; 1];
        self.read_exact(&mut buf)?;
        Ok(buf[0])
    }

    #[inline]
    fn read_i8(&mut self) -> Result<i8> {
        let mut buf = [0; 1];
        self.read_exact(&mut buf)?;
        Ok(buf[0] as i8)
    }

    #[inline]
    fn read_u16_into<T: ByteOrder>(&mut self, dst: &mut [u16]) -> Result<()> {
        {
            let buf = unsafe { slice_to_u8_mut(dst) };
            self.read_exact(buf)?;
        }
        T::from_slice_u16(dst);
        Ok(())
    }
}

impl<R: io::Read + ?Sized> ReadBytesExt for R {}

// ---- B: warpdotdev/warp (fork checkout) crates/warp_terminal/src/focus_env.rs
// @ 325d4d4701b41feb272487e89a6cf02bfba9f194, VERBATIM. Its last three lines
// are the `#[cfg(test)] #[path = "..._tests.rs"] mod tests;` form.
use std::collections::HashMap;
use std::ffi::OsString;

use warp_core::channel::ChannelState;

pub(crate) const FOCUS_URL_ENV: &str = "WARP_FOCUS_URL";
pub(crate) const TERMINAL_SESSION_UUID_ENV: &str = "WARP_TERMINAL_SESSION_UUID";

pub(crate) fn session_focus_url(session_uuid_hex: &str) -> String {
    format!(
        "{}://session/{session_uuid_hex}",
        ChannelState::url_scheme()
    )
}

pub fn add_session_focus_env_vars(env_vars: &mut HashMap<OsString, OsString>, session_uuid: &[u8]) {
    let session_uuid_hex = hex::encode(session_uuid);
    env_vars.insert(
        OsString::from(TERMINAL_SESSION_UUID_ENV),
        OsString::from(session_uuid_hex.clone()),
    );
    env_vars.insert(
        OsString::from(FOCUS_URL_ENV),
        OsString::from(session_focus_url(&session_uuid_hex)),
    );
}

#[cfg(test)]
#[path = "focus_env_tests.rs"]
mod tests;

// ---- C: CONSTRUCTED fixture, for the forms sections A and B lack: a
// bodiless trait declaration (not a mutation target), two impls whose fns share
// a name (distinct symbols), `match` (one decision per arm), nested closures
// (part of the enclosing fn, never symbols of their own), an inline
// `mod tests { }` and a bare `mod tests;`.
pub trait Shape {
    fn area(&self) -> f64;
    fn name(&self) -> String {
        String::from("shape")
    }
}

pub struct Square(f64);
pub struct Circle(f64);

impl Shape for Square {
    fn area(&self) -> f64 {
        self.0 * self.0
    }
}

impl Shape for Circle {
    fn area(&self) -> f64 {
        3.14 * self.0 * self.0
    }
}

impl Square {
    pub fn new(side: f64) -> Self {
        Square(side)
    }
}

pub trait Make {
    fn new() -> Self;
}

impl Make for Square {
    fn new() -> Self {
        Square(1.0)
    }
}

impl fmt::Display for Square {
    fn fmt(&self, f: &mut fmt::Formatter) -> fmt::Result {
        write!(f, "{}", self.0)
    }
}

impl fmt::Debug for Square {
    fn fmt(&self, f: &mut fmt::Formatter) -> fmt::Result {
        write!(f, "Square({})", self.0)
    }
}

impl From<i32> for Circle {
    fn from(n: i32) -> Self {
        Circle(n as f64)
    }
}

impl From<f64> for Circle {
    fn from(n: f64) -> Self {
        Circle(n)
    }
}

pub fn classify(n: i32) -> &'static str {
    match n {
        0 => "zero",
        x if x < 0 => "negative",
        _ => "positive",
    }
}

pub fn scale(xs: &[i32], k: i32) -> Vec<i32> {
    xs.iter().map(|x| (0..k).map(|y| if y > 1 { x * y } else { *x }).sum()).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn zero() {
        assert_eq!(classify(0), "zero");
    }
}

mod tests;
