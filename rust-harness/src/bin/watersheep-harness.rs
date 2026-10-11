use std::fs;
use std::io::{self, Read};
use std::path::PathBuf;

use clap::{Parser, Subcommand};
use watersheep_harness::{
    build_receipt, build_receipt_json, parse_request, WaterSheepClient,
};

#[derive(Debug, Parser)]
#[command(name = "watersheep-harness", version, about = "Typed WaterSheep Rust harness")]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Debug, Subcommand)]
enum Command {
    /// Call a local WaterSheep server and emit a deterministic evidence receipt.
    Ask {
        #[arg(long, default_value = "http://127.0.0.1:8766", env = "WATERSHEEP_BASE_URL")]
        base_url: String,
        #[arg(long, default_value = "-")]
        request: String,
        #[arg(long)]
        out: Option<PathBuf>,
        #[arg(long)]
        response_out: Option<PathBuf>,
    },
    /// Project an already-captured request/response pair without network access.
    Project {
        #[arg(long)]
        request: PathBuf,
        #[arg(long)]
        response: PathBuf,
        #[arg(long, default_value = "offline://watersheep/v1/systemone")]
        endpoint: String,
        #[arg(long)]
        out: Option<PathBuf>,
    },
    /// Validate a WaterSheep System One request without executing inference.
    Validate {
        #[arg(long, default_value = "-")]
        request: String,
    },
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    match Cli::parse().command {
        Command::Ask {
            base_url,
            request,
            out,
            response_out,
        } => {
            let request_text = read_input(&request)?;
            let request = parse_request(&request_text)?;
            let client = WaterSheepClient::new(base_url)?;
            let response = client.ask(&request).await?;
            if let Some(path) = response_out {
                fs::write(path, format!("{}\n", serde_json::to_string_pretty(&response)?))?;
            }
            let receipt = build_receipt(&request, &response, client.endpoint())?;
            write_output(out, &serde_json::to_string_pretty(&receipt)?)?;
        }
        Command::Project {
            request,
            response,
            endpoint,
            out,
        } => {
            let request = fs::read_to_string(request)?;
            let response = fs::read_to_string(response)?;
            let receipt = build_receipt_json(&request, &response, endpoint)?;
            write_output(out, &receipt)?;
        }
        Command::Validate { request } => {
            let request = parse_request(&read_input(&request)?)?;
            println!("{{\"ok\":true,\"questions\":{}}}", request.questions.len());
        }
    }
    Ok(())
}

fn read_input(path: &str) -> io::Result<String> {
    if path == "-" {
        let mut input = String::new();
        io::stdin().read_to_string(&mut input)?;
        Ok(input)
    } else {
        fs::read_to_string(path)
    }
}

fn write_output(path: Option<PathBuf>, body: &str) -> io::Result<()> {
    match path {
        Some(path) => fs::write(path, format!("{body}\n")),
        None => {
            println!("{body}");
            Ok(())
        }
    }
}
