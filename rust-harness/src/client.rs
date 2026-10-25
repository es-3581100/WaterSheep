use std::time::Duration;

use reqwest::StatusCode;

use crate::{
    validate_request, validate_response, HarnessError, SystemOneRequest, SystemOneResponse,
};

#[derive(Clone)]
pub struct WaterSheepClient {
    endpoint: String,
    http: reqwest::Client,
}

impl WaterSheepClient {
    pub fn new(base_url: impl AsRef<str>) -> Result<Self, HarnessError> {
        Self::with_timeout(base_url, Duration::from_secs(120))
    }

    pub fn with_timeout(
        base_url: impl AsRef<str>,
        timeout: Duration,
    ) -> Result<Self, HarnessError> {
        let base = base_url.as_ref().trim().trim_end_matches('/');
        if base.is_empty() {
            return Err(HarnessError::Transport("base URL is empty".into()));
        }
        let endpoint = if base.ends_with("/v1/systemone") {
            base.to_string()
        } else {
            format!("{base}/v1/systemone")
        };
        let http = reqwest::Client::builder()
            .timeout(timeout)
            .build()
            .map_err(|e| HarnessError::Transport(e.to_string()))?;
        Ok(Self { endpoint, http })
    }

    pub fn endpoint(&self) -> &str {
        &self.endpoint
    }

    pub async fn ask(&self, request: &SystemOneRequest) -> Result<SystemOneResponse, HarnessError> {
        validate_request(request)?;
        let response = self
            .http
            .post(&self.endpoint)
            .json(request)
            .send()
            .await
            .map_err(|e| HarnessError::Transport(e.to_string()))?;

        let status = response.status();
        let body = response
            .text()
            .await
            .map_err(|e| HarnessError::Transport(e.to_string()))?;
        if status != StatusCode::OK {
            return Err(HarnessError::Transport(format!(
                "WaterSheep returned HTTP {}: {}",
                status.as_u16(),
                body.trim()
            )));
        }

        let parsed: SystemOneResponse = serde_json::from_str(&body)?;
        validate_response(&parsed)?;
        Ok(parsed)
    }
}
