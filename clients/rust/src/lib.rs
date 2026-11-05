//! Async submission client for fbhook's durable feedback intake API.
//!
//! A successful [`Client::submit`] result means the service accepted and
//! persisted feedback. It does not mean a downstream issue was delivered.

use std::{fmt, time::Duration};

use reqwest::{
    Url,
    header::{CONTENT_TYPE, HeaderValue},
    redirect::Policy,
};
use serde::{Deserialize, Serialize};
use serde_json::Value;

/// The maximum serialized request size accepted by fbhook.
pub const MAX_REQUEST_BYTES: usize = 64 * 1024;
const MAX_RECEIPT_BYTES: usize = 4096;

/// Caller-supplied information about the application producing feedback.
#[derive(Clone, Debug, Default, Eq, PartialEq, Serialize, Deserialize)]
pub struct Source {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub app: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub version: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub environment: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub actor: String,
}

impl Source {
    pub fn new(app: impl Into<String>) -> Self {
        Self {
            app: app.into(),
            ..Self::default()
        }
    }
    pub fn with_version(mut self, version: impl Into<String>) -> Self {
        self.version = version.into();
        self
    }
    pub fn with_environment(mut self, environment: impl Into<String>) -> Self {
        self.environment = environment.into();
        self
    }
    pub fn with_actor(mut self, actor: impl Into<String>) -> Self {
        self.actor = actor.into();
        self
    }
}

/// One feedback submission. Context is an arbitrary native JSON value.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Feedback {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub title: String,
    #[serde(default)]
    pub feedback: String,
    #[serde(default, skip_serializing_if = "source_is_empty")]
    pub source: Source,
    #[serde(
        default,
        deserialize_with = "deserialize_context",
        skip_serializing_if = "Option::is_none"
    )]
    pub context: Option<Value>,
}

fn deserialize_context<'de, D>(deserializer: D) -> Result<Option<Value>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    Value::deserialize(deserializer).map(Some)
}

impl Feedback {
    pub fn new(feedback: impl Into<String>) -> Self {
        Self {
            title: String::new(),
            feedback: feedback.into(),
            source: Source::default(),
            context: None,
        }
    }
    pub fn with_title(mut self, title: impl Into<String>) -> Self {
        self.title = title.into();
        self
    }
    pub fn with_source(mut self, source: Source) -> Self {
        self.source = source;
        self
    }
    pub fn with_context(mut self, context: Value) -> Self {
        self.context = Some(context);
        self
    }

    /// Validates client-side limits without modifying this value.
    pub fn validate(&self) -> Result<(), Error> {
        self.normalized().map(|_| ())
    }

    fn normalized(&self) -> Result<Self, Error> {
        if self.feedback.trim().is_empty() {
            return Err(Error::FeedbackRequired);
        }
        let title = if self.title.trim().is_empty() {
            self.feedback
                .lines()
                .find_map(|line| {
                    let trimmed = line.trim();
                    (!trimmed.is_empty()).then(|| trimmed.chars().take(200).collect())
                })
                .unwrap_or_default()
        } else {
            self.title.clone()
        };
        if title.chars().count() > 200 || title.contains(['\r', '\n']) {
            return Err(Error::InvalidTitle);
        }
        Ok(self.clone())
    }
}

fn source_is_empty(source: &Source) -> bool {
    source.app.is_empty()
        && source.version.is_empty()
        && source.environment.is_empty()
        && source.actor.is_empty()
}

/// The durable-acceptance acknowledgement returned by fbhook.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Receipt {
    pub id: String,
    pub status: String,
}

impl Receipt {
    pub fn new(id: impl Into<String>, status: impl Into<String>) -> Self {
        Self {
            id: id.into(),
            status: status.into(),
        }
    }
}

/// Safe, typed client errors. They intentionally retain neither endpoints nor
/// transport errors, because an endpoint is a credential.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum Error {
    InvalidEndpoint,
    HttpsRequired,
    FeedbackRequired,
    InvalidTitle,
    RequestTooLarge,
    InvalidIdempotencyKey,
    RandomUnavailable,
    RequestFailed,
    HttpStatus(u16),
    InvalidReceipt,
}

impl Error {
    /// Returns the HTTP status for a non-202 server response, if one was received.
    pub fn status(&self) -> Option<u16> {
        match self {
            Self::HttpStatus(status) => Some(*status),
            _ => None,
        }
    }
}

impl fmt::Display for Error {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        let message = match self {
            Self::InvalidEndpoint => "invalid feedback endpoint",
            Self::HttpsRequired => "feedback endpoint must use HTTPS",
            Self::FeedbackRequired => "feedback is required",
            Self::InvalidTitle => "title must be one line of at most 200 characters",
            Self::RequestTooLarge => "feedback request exceeds 64 KiB",
            Self::InvalidIdempotencyKey => "invalid idempotency key",
            Self::RandomUnavailable => "cannot generate idempotency key",
            Self::RequestFailed => "feedback request failed",
            Self::HttpStatus(status) => {
                return write!(formatter, "feedback service returned HTTP {status}");
            }
            Self::InvalidReceipt => "invalid acceptance receipt",
        };
        formatter.write_str(message)
    }
}

impl std::error::Error for Error {}

/// An async client that accepts a complete fbhook intake endpoint.
pub struct Client {
    endpoint: Url,
    http: reqwest::Client,
}

impl fmt::Debug for Client {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.debug_struct("Client").finish_non_exhaustive()
    }
}

impl Client {
    /// Creates a client. HTTP endpoints are limited to exact loopback hosts.
    pub fn new(endpoint: &str) -> Result<Self, Error> {
        let endpoint = Url::parse(endpoint).map_err(|_| Error::InvalidEndpoint)?;
        if endpoint.host_str().is_none()
            || !endpoint.username().is_empty()
            || endpoint.password().is_some()
            || endpoint.query().is_some()
            || endpoint.fragment().is_some()
            || !endpoint.path().starts_with("/api/v1/feedback/")
        {
            return Err(Error::InvalidEndpoint);
        }
        let host = endpoint.host_str().unwrap();
        let loopback = matches!(host, "localhost" | "127.0.0.1" | "[::1]" | "::1");
        match endpoint.scheme() {
            "https" => {}
            "http" if loopback => {}
            "http" => return Err(Error::HttpsRequired),
            _ => return Err(Error::InvalidEndpoint),
        }
        let http = reqwest::Client::builder()
            .redirect(Policy::none())
            .timeout(Duration::from_secs(15))
            .build()
            .map_err(|_| Error::RequestFailed)?;
        Ok(Self { endpoint, http })
    }

    /// Submits one immutable serialized request, retrying only transient intake failures.
    pub async fn submit(&self, feedback: &Feedback, key: Option<&str>) -> Result<Receipt, Error> {
        let body =
            serde_json::to_vec(&feedback.normalized()?).map_err(|_| Error::InvalidReceipt)?;
        if body.len() > MAX_REQUEST_BYTES {
            return Err(Error::RequestTooLarge);
        }
        let key = match key {
            Some(key) if !key.is_empty() => key.to_owned(),
            _ => generated_key()?,
        };
        if key.len() > 200 || key.contains(['\r', '\n']) || HeaderValue::from_str(&key).is_err() {
            return Err(Error::InvalidIdempotencyKey);
        }

        for attempt in 0..3 {
            let response = self
                .http
                .post(self.endpoint.clone())
                .header(CONTENT_TYPE, "application/json")
                .header("Idempotency-Key", &key)
                .body(body.clone())
                .send()
                .await;
            let (error, retry) = match response {
                Err(_) => (Error::RequestFailed, true),
                Ok(response) => {
                    let status = response.status().as_u16();
                    match read_limited(response).await {
                        Err(error) if status == 202 => (error, true),
                        Err(_) => (Error::HttpStatus(status), status == 429 || status >= 500),
                        Ok((bytes, oversized)) if status == 202 => {
                            if oversized {
                                return Err(Error::InvalidReceipt);
                            }
                            let receipt = serde_json::from_slice::<Receipt>(&bytes)
                                .map_err(|_| Error::InvalidReceipt)?;
                            if receipt.id.is_empty() || receipt.status != "accepted" {
                                return Err(Error::InvalidReceipt);
                            }
                            return Ok(receipt);
                        }
                        Ok(_) => (Error::HttpStatus(status), status == 429 || status >= 500),
                    }
                }
            };
            if !retry || attempt == 2 {
                return Err(error);
            }
            tokio::time::sleep(Duration::from_millis(100 * (attempt + 1))).await;
        }
        unreachable!("three attempts either return or fail")
    }
}

async fn read_limited(mut response: reqwest::Response) -> Result<(Vec<u8>, bool), Error> {
    let mut bytes = Vec::new();
    while let Some(chunk) = response.chunk().await.map_err(|_| Error::RequestFailed)? {
        let remaining = MAX_RECEIPT_BYTES
            .saturating_add(1)
            .saturating_sub(bytes.len());
        bytes.extend_from_slice(&chunk[..chunk.len().min(remaining)]);
        if bytes.len() > MAX_RECEIPT_BYTES {
            return Ok((bytes, true));
        }
    }
    Ok((bytes, false))
}

fn generated_key() -> Result<String, Error> {
    let mut bytes = [0_u8; 32];
    getrandom::fill(&mut bytes).map_err(|_| Error::RandomUnavailable)?;
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut key = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        key.push(HEX[(byte >> 4) as usize] as char);
        key.push(HEX[(byte & 0x0f) as usize] as char);
    }
    Ok(key)
}

/// Overlays nonempty submission fields on defaults. Context replaces defaults and
/// all owned values are cloned, so neither input can be mutated through output.
pub fn prepare(defaults: &Feedback, submission: &Feedback) -> Feedback {
    let mut output = defaults.clone();
    if !submission.title.is_empty() {
        output.title = submission.title.clone();
    }
    if !submission.feedback.is_empty() {
        output.feedback = submission.feedback.clone();
    }
    if !submission.source.app.is_empty() {
        output.source.app = submission.source.app.clone();
    }
    if !submission.source.version.is_empty() {
        output.source.version = submission.source.version.clone();
    }
    if !submission.source.environment.is_empty() {
        output.source.environment = submission.source.environment.clone();
    }
    if !submission.source.actor.is_empty() {
        output.source.actor = submission.source.actor.clone();
    }
    if submission.context.is_some() {
        output.context = submission.context.clone();
    }
    output
}
