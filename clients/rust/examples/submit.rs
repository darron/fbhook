use std::env;

use fbhook::{Client, Feedback, Source};
use serde_json::json;

#[tokio::main(flavor = "current_thread")]
async fn main() {
    let endpoint = required("FBHOOK_ENDPOINT");
    let key = required("FBHOOK_IDEMPOTENCY_KEY");
    let client = match Client::new(&endpoint) {
        Ok(client) => client,
        Err(error) => fail(error),
    };
    let feedback = Feedback::new("Keep search filters when returning to results")
        .with_source(Source::new("example-app").with_actor("backend"))
        .with_context(json!({"page": "/search"}));
    match client.submit(&feedback, Some(&key)).await {
        Ok(receipt) => println!(
            "{}",
            serde_json::json!({"id": receipt.id, "status": receipt.status})
        ),
        Err(error) => fail(error),
    }
}

fn required(name: &str) -> String {
    match env::var(name) {
        Ok(value) if !value.is_empty() => value,
        _ => {
            eprintln!("{name} must be set");
            std::process::exit(2);
        }
    }
}

fn fail(error: fbhook::Error) -> ! {
    eprintln!("feedback submission failed: {error}");
    std::process::exit(1);
}
