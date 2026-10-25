use wasm_bindgen::prelude::*;

use crate::{build_receipt_json, parse_request};

#[wasm_bindgen]
pub fn validate_request_json(input: &str) -> Result<String, JsValue> {
    let request = parse_request(input).map_err(js_error)?;
    Ok(format!("ok:{}", request.questions.len()))
}

#[wasm_bindgen]
pub fn project_receipt(
    request_json: &str,
    response_json: &str,
    endpoint: &str,
) -> Result<String, JsValue> {
    build_receipt_json(request_json, response_json, endpoint).map_err(js_error)
}

fn js_error(error: impl ToString) -> JsValue {
    JsValue::from_str(&error.to_string())
}
