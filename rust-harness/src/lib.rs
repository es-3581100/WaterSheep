//! WaterSheep Rust harness.
//!
//! WaterSheep keeps inference authority. This crate owns the typed boundary around
//! requests, responses, deterministic receipts, and evidence projection.

pub mod contracts;
mod receipt;

#[cfg(feature = "native")]
pub mod client;
#[cfg(feature = "native")]
pub use client::WaterSheepClient;

#[cfg(feature = "wasm")]
mod wasm;

pub use contracts::{
    Answer, Authority, HarnessReceipt, MatrixDecision, QuestionSpec, QuestionType,
    SystemOneRequest, SystemOneResponse, Usage,
};
pub use receipt::{
    build_receipt, build_receipt_json, parse_request, parse_response, validate_request,
    validate_response, HarnessError,
};
