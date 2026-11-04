use fbhook::{Client, Error, Feedback, Receipt, Source, prepare};
use serde_json::json;
use std::{
    io::{Read, Write},
    net::TcpListener,
    sync::{Arc, Mutex},
    thread,
    time::Duration,
};
#[derive(Clone)]
struct Reply {
    status: u16,
    body: Vec<u8>,
    length: Option<usize>,
    location: Option<&'static str>,
}
#[derive(Clone)]
struct Request {
    raw: Vec<u8>,
    key: String,
}
fn server(replies: Vec<Reply>) -> (String, Arc<Mutex<Vec<Request>>>) {
    let l = TcpListener::bind("127.0.0.1:0").unwrap();
    let url = format!(
        "http://{}/api/v1/feedback/test.secret",
        l.local_addr().unwrap()
    );
    let r = Arc::new(Mutex::new(vec![]));
    let out = r.clone();
    thread::spawn(move || {
        for x in replies {
            let (mut s, _) = l.accept().unwrap();
            out.lock().unwrap().push(read(&mut s));
            write_reply(&mut s, x)
        }
    });
    (url, r)
}
fn read(s: &mut std::net::TcpStream) -> Request {
    s.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
    let mut b = vec![];
    let mut x = [0; 1024];
    let e = loop {
        let n = s.read(&mut x).unwrap();
        b.extend_from_slice(&x[..n]);
        if let Some(i) = b.windows(4).position(|z| z == b"\r\n\r\n") {
            break i + 4;
        }
    };
    let h = String::from_utf8_lossy(&b[..e]).into_owned();
    let n = h
        .lines()
        .find_map(|z| {
            z.split_once(':')
                .filter(|(k, _)| k.eq_ignore_ascii_case("content-length"))
                .map(|(_, v)| v.trim().parse::<usize>().unwrap())
        })
        .unwrap();
    while b.len() < e + n {
        let n = s.read(&mut x).unwrap();
        b.extend_from_slice(&x[..n])
    }
    let k = h
        .lines()
        .find_map(|z| {
            z.split_once(':')
                .filter(|(k, _)| k.eq_ignore_ascii_case("idempotency-key"))
                .map(|(_, v)| v.trim().to_owned())
        })
        .unwrap();
    Request {
        raw: b[..e + n].to_vec(),
        key: k,
    }
}
fn write_reply(s: &mut std::net::TcpStream, r: Reply) {
    let p = match r.status {
        202 => "Accepted",
        302 => "Found",
        409 => "Conflict",
        429 => "Too Many",
        503 => "Unavailable",
        _ => "Bad",
    };
    let l = r.length.unwrap_or(r.body.len());
    let loc = r
        .location
        .map(|v| format!("Location: {v}\r\n"))
        .unwrap_or_default();
    write!(
        s,
        "HTTP/1.1 {} {}\r\nContent-Length: {}\r\n{}Connection: close\r\n\r\n",
        r.status, p, l, loc
    )
    .unwrap();
    s.write_all(&r.body).unwrap()
}
fn ok(id: &str) -> Reply {
    Reply {
        status: 202,
        body: format!(r#"{{"id":"{id}","status":"accepted"}}"#).into_bytes(),
        length: None,
        location: None,
    }
}
fn fb() -> Feedback {
    Feedback::new("hello").with_context(json!({"n":9007199254740993u64}))
}
#[tokio::test]
async fn accepts() {
    let (u, r) = server(vec![ok("a")]);
    assert_eq!(
        Client::new(&u)
            .unwrap()
            .submit(&fb(), Some("k"))
            .await
            .unwrap(),
        Receipt::new("a", "accepted")
    );
    assert_eq!(r.lock().unwrap().len(), 1)
}
#[tokio::test]
async fn retries_and_stable_generated_key() {
    let mut cut = ok("x");
    cut.length = Some(cut.body.len() + 1);
    let (u, r) = server(vec![
        Reply {
            status: 429,
            body: vec![],
            length: None,
            location: None,
        },
        Reply {
            status: 503,
            body: vec![],
            length: None,
            location: None,
        },
        cut,
    ]);
    assert_eq!(
        Client::new(&u)
            .unwrap()
            .submit(&fb(), None)
            .await
            .unwrap_err(),
        Error::RequestFailed
    );
    let x = r.lock().unwrap();
    assert_eq!(x.len(), 3);
    assert!(!x[0].key.is_empty());
    assert!(
        x.windows(2)
            .all(|w| w[0].key == w[1].key && w[0].raw == w[1].raw)
    )
}
#[tokio::test]
async fn empty_key_and_truncated_receipt_retry() {
    let mut cut = ok("x");
    cut.length = Some(cut.body.len() + 1);
    let (u, r) = server(vec![cut, ok("b")]);
    assert_eq!(
        Client::new(&u)
            .unwrap()
            .submit(&fb(), Some(""))
            .await
            .unwrap()
            .id,
        "b"
    );
    let x = r.lock().unwrap();
    assert_eq!(x.len(), 2);
    assert!(!x[0].key.is_empty());
    assert_eq!(x[0].key, x[1].key)
}
#[tokio::test]
async fn max_three_and_no_retry_bad_responses() {
    let (u, r) = server(vec![
        Reply {
            status: 503,
            body: vec![],
            length: None,
            location: None
        };
        3
    ]);
    assert_eq!(
        Client::new(&u)
            .unwrap()
            .submit(&fb(), Some("k"))
            .await
            .unwrap_err()
            .status(),
        Some(503)
    );
    assert_eq!(r.lock().unwrap().len(), 3);
    for q in [
        Reply {
            status: 409,
            body: vec![],
            length: None,
            location: None,
        },
        Reply {
            status: 302,
            body: vec![],
            length: None,
            location: Some("/x"),
        },
        Reply {
            status: 202,
            body: b"bad".to_vec(),
            length: None,
            location: None,
        },
    ] {
        let (u, r) = server(vec![q]);
        assert!(
            Client::new(&u)
                .unwrap()
                .submit(&fb(), Some("k"))
                .await
                .is_err()
        );
        assert_eq!(r.lock().unwrap().len(), 1)
    }
}
#[tokio::test]
async fn bounds_and_cancels() {
    let (u, r) = server(vec![Reply {
        status: 202,
        body: vec![b'x'; 4097],
        length: None,
        location: None,
    }]);
    assert_eq!(
        Client::new(&u)
            .unwrap()
            .submit(&fb(), Some("k"))
            .await
            .unwrap_err(),
        Error::InvalidReceipt
    );
    assert_eq!(r.lock().unwrap().len(), 1);
    let (u, r) = server(vec![Reply {
        status: 503,
        body: vec![],
        length: None,
        location: None,
    }]);
    let c = Client::new(&u).unwrap();
    assert!(
        tokio::time::timeout(Duration::from_millis(20), c.submit(&fb(), Some("k")))
            .await
            .is_err()
    );
    tokio::time::sleep(Duration::from_millis(130)).await;
    assert_eq!(r.lock().unwrap().len(), 1)
}
#[test]
fn validation_and_prepare() {
    for u in [
        "http://example.com/api/v1/feedback/a.b",
        "http://localhost.example.com/api/v1/feedback/a.b",
        "https://user:x@example.com/api/v1/feedback/a.b",
        "https://e.com/api/v1/feedback/a.b?q=x",
    ] {
        assert!(Client::new(u).is_err())
    }
    assert!(Client::new("http://[::1]:1/api/v1/feedback/a.b").is_ok());
    assert!(fbhook::Feedback::new(" ").validate().is_err());
    let s: Source = serde_json::from_str(r#"{"app":"a"}"#).unwrap();
    assert_eq!(s.version, "");
    let a = Feedback::new("a").with_context(json!({"x":1}));
    let b: Feedback = serde_json::from_str(r#"{"feedback":"b","context":null}"#).unwrap();
    assert_eq!(prepare(&a, &b).context, Some(json!({"x":1})))
}
