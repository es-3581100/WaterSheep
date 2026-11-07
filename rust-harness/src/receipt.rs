use std::collections::{BTreeMap, BTreeSet};

use serde::Serialize;
use serde_json::{Map, Value};
use sha2::{Digest, Sha256};
use thiserror::Error;

use crate::contracts::{
    Answer, Authority, HarnessReceipt, MatrixDecision, QuestionType, SystemOneRequest,
    SystemOneResponse,
};

#[derive(Debug, Error)]
pub enum HarnessError {
    #[error("invalid JSON: {0}")]
    Json(#[from] serde_json::Error),
    #[error("invalid request: {0}")]
    InvalidRequest(String),
    #[error("invalid response: {0}")]
    InvalidResponse(String),
    #[error("transport error: {0}")]
    Transport(String),
}

pub fn parse_request(input: &str) -> Result<SystemOneRequest, HarnessError> {
    let request: SystemOneRequest = serde_json::from_str(input)?;
    validate_request(&request)?;
    Ok(request)
}

pub fn parse_response(input: &str) -> Result<SystemOneResponse, HarnessError> {
    let response: SystemOneResponse = serde_json::from_str(input)?;
    validate_response(&response)?;
    Ok(response)
}

pub fn validate_request(request: &SystemOneRequest) -> Result<(), HarnessError> {
    if request.questions.is_empty() {
        return Err(HarnessError::InvalidRequest(
            "questions must contain at least one entry".into(),
        ));
    }

    for (name, question) in &request.questions {
        if name.trim().is_empty() {
            return Err(HarnessError::InvalidRequest(
                "question names must not be empty".into(),
            ));
        }
        if question.instructions.trim().is_empty() {
            return Err(HarnessError::InvalidRequest(format!(
                "question {name:?} has empty instructions"
            )));
        }
        if let Some(threshold) = question.threshold {
            if !threshold.is_finite() || !(0.0..=1.0).contains(&threshold) {
                return Err(HarnessError::InvalidRequest(format!(
                    "question {name:?} threshold must be between 0 and 1"
                )));
            }
            if question.kind != QuestionType::Multi {
                return Err(HarnessError::InvalidRequest(format!(
                    "question {name:?} sets threshold but is not multi"
                )));
            }
        }
    }
    Ok(())
}

pub fn validate_response(response: &SystemOneResponse) -> Result<(), HarnessError> {
    if response.model.trim().is_empty() {
        return Err(HarnessError::InvalidResponse("model is empty".into()));
    }

    for (name, answer) in &response.answers {
        validate_answer(name, answer)?;
    }
    Ok(())
}

fn validate_answer(name: &str, answer: &Answer) -> Result<(), HarnessError> {
    if let Some(confidence) = answer.confidence {
        probability(name, "confidence", confidence)?;
    }
    for (label, value) in &answer.probabilities {
        probability(name, &format!("probabilities[{label:?}]"), *value)?;
    }

    if answer.error.is_some() {
        return Ok(());
    }

    match answer.kind.as_str() {
        "noul" => {
            let p = answer.noul.ok_or_else(|| {
                HarnessError::InvalidResponse(format!(
                    "answer {name:?} is noul but has no noul value"
                ))
            })?;
            probability(name, "noul", p)?;
        }
        "choice" => {
            if answer.choice.as_deref().unwrap_or("").is_empty() {
                return Err(HarnessError::InvalidResponse(format!(
                    "answer {name:?} is choice but has no selected choice"
                )));
            }
        }
        "score" => {
            let score = answer.score.ok_or_else(|| {
                HarnessError::InvalidResponse(format!("answer {name:?} is score but has no score"))
            })?;
            if !score.is_finite() {
                return Err(HarnessError::InvalidResponse(format!(
                    "answer {name:?} score is not finite"
                )));
            }
        }
        "multi" => {}
        other => {
            return Err(HarnessError::InvalidResponse(format!(
                "answer {name:?} has unsupported type {other:?}"
            )))
        }
    }
    Ok(())
}

fn probability(name: &str, field: &str, value: f64) -> Result<(), HarnessError> {
    if !value.is_finite() || !(0.0..=1.0).contains(&value) {
        return Err(HarnessError::InvalidResponse(format!(
            "answer {name:?} {field} must be between 0 and 1"
        )));
    }
    Ok(())
}

pub fn build_receipt(
    request: &SystemOneRequest,
    response: &SystemOneResponse,
    endpoint: impl Into<String>,
) -> Result<HarnessReceipt, HarnessError> {
    validate_request(request)?;
    validate_response(response)?;

    let requested: BTreeSet<_> = request.questions.keys().collect();
    let answered: BTreeSet<_> = response.answers.keys().collect();
    if requested != answered {
        return Err(HarnessError::InvalidResponse(
            "answer names do not exactly match request question names".into(),
        ));
    }

    let decisions = response
        .answers
        .iter()
        .map(|(name, answer)| project_answer(name, answer))
        .collect();

    Ok(HarnessReceipt {
        schema: "watersheep.harness.receipt/v1".into(),
        producer: "watersheep-rust-harness".into(),
        authority: Authority::EvidenceOnly,
        endpoint: endpoint.into(),
        request_sha256: hash_json(request)?,
        response_sha256: hash_json(response)?,
        model: response.model.clone(),
        usage: response.usage.clone(),
        decisions,
    })
}

pub fn build_receipt_json(
    request_json: &str,
    response_json: &str,
    endpoint: impl Into<String>,
) -> Result<String, HarnessError> {
    let request = parse_request(request_json)?;
    let response = parse_response(response_json)?;
    let receipt = build_receipt(&request, &response, endpoint)?;
    Ok(serde_json::to_string_pretty(&receipt)?)
}

fn project_answer(name: &str, answer: &Answer) -> MatrixDecision {
    let mut probabilities = answer.probabilities.clone();
    let mut selected = Vec::new();
    let mut scalar = None;
    let mut confidence = answer.confidence;

    if answer.error.is_none() {
        match answer.kind.as_str() {
            "noul" => {
                if let Some(p_yes) = answer.noul {
                    probabilities.insert("yes".into(), p_yes);
                    probabilities.insert("no".into(), 1.0 - p_yes);
                    selected.push(if p_yes >= 0.5 { "yes" } else { "no" }.into());
                    scalar = Some(p_yes);
                    confidence = Some(p_yes.max(1.0 - p_yes));
                }
            }
            "choice" => {
                if let Some(choice) = &answer.choice {
                    selected.push(choice.clone());
                }
            }
            "score" => {
                scalar = answer.score;
            }
            "multi" => selected = answer.choices.clone(),
            _ => {}
        }
    }

    MatrixDecision {
        name: name.into(),
        decision_type: answer.kind.clone(),
        status: if answer.error.is_some() {
            "error"
        } else {
            "ok"
        }
        .into(),
        selected,
        scalar,
        confidence,
        probabilities,
        legend: answer.legend.clone(),
        truncated: answer.truncated,
        error: answer.error.clone(),
    }
}

fn hash_json<T: Serialize>(value: &T) -> Result<String, HarnessError> {
    let value = serde_json::to_value(value)?;
    let canonical = canonicalize(value);
    let bytes = serde_json::to_vec(&canonical)?;
    let digest = Sha256::digest(bytes);
    Ok(hex::encode(digest))
}

fn canonicalize(value: Value) -> Value {
    match value {
        Value::Array(items) => Value::Array(items.into_iter().map(canonicalize).collect()),
        Value::Object(map) => {
            let sorted: BTreeMap<_, _> = map
                .into_iter()
                .map(|(key, value)| (key, canonicalize(value)))
                .collect();
            let mut out = Map::new();
            for (key, value) in sorted {
                out.insert(key, value);
            }
            Value::Object(out)
        }
        other => other,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn request() -> SystemOneRequest {
        serde_json::from_str(
            r#"{
              "state":{"message":"charged twice","customer":"Priya"},
              "questions":{"team":{"type":"choice","instructions":"Which team?","criteria":["billing","shipping"]}}
            }"#,
        )
        .unwrap()
    }

    fn response() -> SystemOneResponse {
        serde_json::from_str(
            r#"{
              "model":"watersheep-test",
              "answers":{"team":{"type":"choice","choice":"billing","confidence":0.91,"probabilities":{"billing":0.91,"shipping":0.09}}},
              "usage":{"input_tokens":17,"output_tokens":0}
            }"#,
        )
        .unwrap()
    }

    #[test]
    fn receipt_is_evidence_only_and_projects_choice() {
        let receipt = build_receipt(
            &request(),
            &response(),
            "http://127.0.0.1:8766/v1/systemone",
        )
        .unwrap();
        assert_eq!(receipt.schema, "watersheep.harness.receipt/v1");
        assert_eq!(receipt.authority, Authority::EvidenceOnly);
        assert_eq!(receipt.decisions[0].selected, vec!["billing".to_string()]);
        assert_eq!(receipt.decisions[0].confidence, Some(0.91));
    }

    #[test]
    fn canonical_hash_does_not_depend_on_object_key_order() {
        let a = parse_request(
            r#"{"state":{"b":2,"a":1},"questions":{"x":{"type":"noul","instructions":"ok?"}}}"#,
        )
        .unwrap();
        let b = parse_request(
            r#"{"questions":{"x":{"instructions":"ok?","type":"noul"}},"state":{"a":1,"b":2}}"#,
        )
        .unwrap();
        assert_eq!(hash_json(&a).unwrap(), hash_json(&b).unwrap());
    }

    #[test]
    fn stale_response_is_rejected() {
        let mut r = response();
        r.answers.insert(
            "other".into(),
            Answer {
                kind: "noul".into(),
                noul: Some(0.5),
                ..Answer::default()
            },
        );
        let err = build_receipt(&request(), &r, "offline").unwrap_err();
        assert!(err.to_string().contains("exactly match"));
    }

    #[test]
    fn score_projection_preserves_legend_and_truncation() {
        let answer = Answer {
            kind: "score".into(),
            score: Some(1.25),
            confidence: Some(0.8),
            probabilities: BTreeMap::from([("0".into(), 0.2), ("1".into(), 0.8)]),
            legend: BTreeMap::from([("0".into(), "low".into()), ("1".into(), "high".into())]),
            truncated: Some(true),
            ..Answer::default()
        };
        let decision = project_answer("rating", &answer);
        assert_eq!(decision.scalar, Some(1.25));
        assert_eq!(decision.legend.get("1").map(String::as_str), Some("high"));
        assert_eq!(decision.truncated, Some(true));
    }

    #[test]
    fn invalid_probability_is_rejected() {
        let mut r = response();
        r.answers.get_mut("team").unwrap().confidence = Some(1.2);
        assert!(validate_response(&r).is_err());
    }
}
